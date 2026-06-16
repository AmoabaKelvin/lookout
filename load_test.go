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
	samples, err := loadCollector(writeLoadavg(t, "0.12 0.34 0.56 1/234 5678\n"))
	if err != nil {
		t.Fatal(err)
	}

	values := metricValues(samples)
	assertFloatNear(t, values["load.1"], 0.12)
	assertFloatNear(t, values["load.5"], 0.34)
	assertFloatNear(t, values["load.15"], 0.56)

	for _, sample := range samples {
		if sample.Collector != "load" {
			t.Fatalf("%s collector = %q, want load", sample.Name, sample.Collector)
		}
		if sample.Unit != "load" {
			t.Fatalf("%s unit = %q, want load", sample.Name, sample.Unit)
		}
		if sample.Timestamp.IsZero() {
			t.Fatalf("%s timestamp was not set", sample.Name)
		}
	}
}

func TestLoadCollectorRejectsShortLoadavg(t *testing.T) {
	_, err := loadCollector(writeLoadavg(t, "0.12 0.34\n"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "expected at least 3 load average fields") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadCollectorRejectsInvalidLoadavg(t *testing.T) {
	_, err := loadCollector(writeLoadavg(t, "0.12 nope 0.56 1/234 5678\n"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), `field 2 value "nope"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadCollectorReadErrorIncludesPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")

	_, err := loadCollector(path)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "failed to read "+path) {
		t.Fatalf("unexpected error: %v", err)
	}
}
