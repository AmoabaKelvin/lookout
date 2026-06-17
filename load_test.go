package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLoadavg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "loadavg")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadCollectorParsesLoadavg(t *testing.T) {
	samples, err := loadCollector(writeLoadavg(t, "2.00 4.00 8.00 1/234 5678\n"), 4)
	if err != nil {
		t.Fatal(err)
	}

	values := metricValues(samples)
	assertFloatNear(t, values["load.1"], 2)
	assertFloatNear(t, values["load.5"], 4)
	assertFloatNear(t, values["load.15"], 8)
	assertFloatNear(t, values["load.1_per_core"], 0.5)
	assertFloatNear(t, values["load.5_per_core"], 1)
	assertFloatNear(t, values["load.15_per_core"], 2)

	for _, sample := range samples {
		if sample.Collector != "load" {
			t.Fatalf("%s collector = %q, want load", sample.Name, sample.Collector)
		}
		if strings.HasSuffix(sample.Name, "_per_core") {
			if sample.Unit != "load/core" {
				t.Fatalf("%s unit = %q, want load/core", sample.Name, sample.Unit)
			}
		} else if sample.Unit != "load" {
			t.Fatalf("%s unit = %q, want load", sample.Name, sample.Unit)
		}
		if sample.Timestamp.IsZero() {
			t.Fatalf("%s timestamp was not set", sample.Name)
		}
	}
}

func TestLoadCollectorRejectsShortLoadavg(t *testing.T) {
	_, err := loadCollector(writeLoadavg(t, "0.12 0.34\n"), 1)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "expected at least 3 load average fields") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadCollectorRejectsInvalidLoadavg(t *testing.T) {
	_, err := loadCollector(writeLoadavg(t, "0.12 nope 0.56 1/234 5678\n"), 1)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), `field 2 value "nope"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadCollectorReadErrorIncludesPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")

	_, err := loadCollector(path, 1)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "failed to read "+path) {
		t.Fatalf("unexpected error: %v", err)
	}
}
