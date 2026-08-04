package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func copyPressureFixture(t *testing.T, sourceDir, targetDir string) {
	t.Helper()
	for _, name := range []string{"cpu", "memory", "io"} {
		body, err := os.ReadFile(filepath.Join(sourceDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(targetDir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParsePressureResourceAllowsCPUWithoutFull(t *testing.T) {
	resource, err := parsePressureResource([]byte("some avg10=2.50 avg60=1.25 avg300=0.50 total=12345\n"), "cpu", false)
	if err != nil {
		t.Fatal(err)
	}
	if resource.Some == nil || resource.Full != nil {
		t.Fatalf("unexpected CPU pressure resource: %+v", resource)
	}
	assertFloatNear(t, resource.Some.Avg10, 2.5)
	if resource.Some.Total != 12345 {
		t.Fatalf("total = %d, want 12345", resource.Some.Total)
	}
}

func TestParsePressureResourceRequiresMemoryAndIOFull(t *testing.T) {
	_, err := parsePressureResource([]byte("some avg10=0 avg60=0 avg300=0 total=0\n"), "memory", true)
	if err == nil || !strings.Contains(err.Error(), "missing full pressure line") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParsePressureResourceRejectsMalformedValues(t *testing.T) {
	_, err := parsePressureResource([]byte("some avg10=nope avg60=0 avg300=0 total=0\n"), "cpu", false)
	if err == nil || !strings.Contains(err.Error(), "cpu:1") || !strings.Contains(err.Error(), "avg10") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPressureCollectorReportsAveragesAndIntervalStall(t *testing.T) {
	dir := t.TempDir()
	copyPressureFixture(t, "testdata/pressure-low", dir)

	collector, err := newPressureCollector(dir)
	if err != nil {
		t.Fatal(err)
	}
	currentTime := time.Unix(1_700_000_000, 0)
	collector.now = func() time.Time { return currentTime }

	first, err := collector.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 15 {
		t.Fatalf("first collection samples = %d, want 15 averages", len(first))
	}
	firstValues := metricValues(first)
	assertFloatNear(t, firstValues["pressure.cpu.some.avg10_percent"], 0)
	if _, ok := firstValues["pressure.cpu.some.stall_percent"]; ok {
		t.Fatal("first collection should only establish the interval baseline")
	}

	currentTime = currentTime.Add(10 * time.Second)
	copyPressureFixture(t, "testdata/pressure-high", dir)
	second, err := collector.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 20 {
		t.Fatalf("second collection samples = %d, want 20", len(second))
	}
	values := metricValues(second)
	assertFloatNear(t, values["pressure.cpu.some.avg10_percent"], 60)
	assertFloatNear(t, values["pressure.cpu.some.stall_percent"], 30)
	assertFloatNear(t, values["pressure.memory.some.stall_percent"], 30)
	assertFloatNear(t, values["pressure.memory.full.stall_percent"], 20)
	assertFloatNear(t, values["pressure.io.some.stall_percent"], 30)
	assertFloatNear(t, values["pressure.io.full.stall_percent"], 20)

	for _, sample := range second {
		if sample.Collector != "pressure" || sample.Timestamp != currentTime {
			t.Fatalf("unexpected pressure sample metadata: %+v", sample)
		}
	}
}

func TestNewPressureCollectorReportsUnavailablePSI(t *testing.T) {
	_, err := newPressureCollector(t.TempDir())
	if err == nil {
		t.Fatal("expected missing pressure files to fail the probe")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error should preserve os.ErrNotExist, got %v", err)
	}
}

func TestConfiguredPressureCollectorRequiresPSIOnlyForAlerts(t *testing.T) {
	missingDir := t.TempDir()
	cfg := defaultConfig()

	collector, err := configuredPressureCollector(cfg, missingDir)
	if err != nil || collector != nil {
		t.Fatalf("disabled pressure collector = %v, err = %v", collector, err)
	}

	cfg.Metrics.Enabled = true
	collector, err = configuredPressureCollector(cfg, missingDir)
	if err != nil || collector != nil {
		t.Fatalf("optional metrics-only pressure collector = %v, err = %v", collector, err)
	}

	cfg.Alerts.Pressure.Enabled = true
	_, err = configuredPressureCollector(cfg, missingDir)
	if err == nil || !strings.Contains(err.Error(), "PSI is unavailable") {
		t.Fatalf("expected actionable unavailable PSI error, got %v", err)
	}

	availableDir := t.TempDir()
	copyPressureFixture(t, "testdata/pressure-low", availableDir)
	collector, err = configuredPressureCollector(cfg, availableDir)
	if err != nil || collector == nil {
		t.Fatalf("available pressure collector = %v, err = %v", collector, err)
	}
}

func TestPressureCollectorAgainstRealProc(t *testing.T) {
	if os.Getenv("LOOKOUT_TEST_REAL_PSI") != "1" {
		t.Skip("set LOOKOUT_TEST_REAL_PSI=1 on Linux to test the live PSI interface")
	}

	collector, err := newPressureCollector("/proc/pressure")
	if err != nil {
		t.Fatal(err)
	}
	samples, err := collector.Collect()
	if err != nil {
		t.Fatal(err)
	}
	values := metricValues(samples)
	for _, name := range []string{
		"pressure.cpu.some.avg10_percent",
		"pressure.memory.some.avg10_percent",
		"pressure.memory.full.avg10_percent",
		"pressure.io.some.avg10_percent",
		"pressure.io.full.avg10_percent",
	} {
		if _, ok := values[name]; !ok {
			t.Errorf("live PSI collection missing %s", name)
		}
	}
}
