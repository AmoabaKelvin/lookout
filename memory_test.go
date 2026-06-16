package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMemInfo(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func metricValues(samples []MetricSample) map[string]float64 {
	values := make(map[string]float64, len(samples))
	for _, sample := range samples {
		values[sample.Name] = sample.Value
	}
	return values
}

func assertFloatNear(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.001 {
		t.Fatalf("got %.6f, want %.6f", got, want)
	}
}

func TestMemoryCollectorParsesMemInfo(t *testing.T) {
	samples, err := memoryCollector("testdata/meminfo.txt")
	if err != nil {
		t.Fatal(err)
	}

	values := metricValues(samples)
	assertFloatNear(t, values["memory.total"], 8185712)
	assertFloatNear(t, values["memory.available"], 7638540)
	assertFloatNear(t, values["memory.used"], 547172)
	assertFloatNear(t, values["memory.used_percent"], 6.684474)

	for _, sample := range samples {
		if sample.Collector != "memory" {
			t.Fatalf("%s collector = %q, want memory", sample.Name, sample.Collector)
		}
		if sample.Timestamp.IsZero() {
			t.Fatalf("%s timestamp was not set", sample.Name)
		}
	}
}

func TestMemoryCollectorFallsBackWhenMemAvailableMissing(t *testing.T) {
	path := writeMemInfo(t, `MemTotal: 1000 kB
MemFree: 100 kB
Buffers: 50 kB
Cached: 300 kB
SReclaimable: 25 kB
Shmem: 75 kB
`)

	samples, err := memoryCollector(path)
	if err != nil {
		t.Fatal(err)
	}

	values := metricValues(samples)
	assertFloatNear(t, values["memory.available"], 400)
	assertFloatNear(t, values["memory.used"], 600)
	assertFloatNear(t, values["memory.used_percent"], 60)
}

func TestMemoryCollectorMissingRequiredFieldErrors(t *testing.T) {
	path := writeMemInfo(t, "MemAvailable: 100 kB\n")

	_, err := memoryCollector(path)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "missing required memory field MemTotal") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMemoryCollectorParseErrorIncludesSourceAndLine(t *testing.T) {
	path := writeMemInfo(t, "MemTotal: 1000 kB\nMemAvailable: nope kB\n")

	_, err := memoryCollector(path)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), path+":2") || !strings.Contains(err.Error(), "MemAvailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMemoryCollectorReadErrorIncludesPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")

	_, err := memoryCollector(path)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "failed to read "+path) {
		t.Fatalf("unexpected error: %v", err)
	}
}
