package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProcStat(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stat")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCPUCollectorUsesDeltas(t *testing.T) {
	first := writeProcStat(t, "cpu  100 0 100 800 0 0 0 0 0 0\n")
	second := writeProcStat(t, "cpu  200 0 200 900 0 0 0 0 0 0\n")

	samples, previous, err := cpuCollector(first, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 0 {
		t.Fatalf("first sample should only establish baseline, got %+v", samples)
	}

	samples, _, err = cpuCollector(second, previous)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 {
		t.Fatalf("samples = %d, want 1", len(samples))
	}
	assertFloatNear(t, samples[0].Value, 66.666666)
	if samples[0].Name != "cpu.used_percent" || samples[0].Collector != "cpu" {
		t.Fatalf("unexpected cpu sample: %+v", samples[0])
	}
}

func TestCPUCollectorRejectsInvalidStat(t *testing.T) {
	_, _, err := cpuCollector(writeProcStat(t, "cpu nope 0 0 0\n"), nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "parsing cpu field 1") {
		t.Fatalf("unexpected error: %v", err)
	}
}
