package main

import (
	"testing"
	"time"
)

func TestDiskCollectorCollectsTargetMount(t *testing.T) {
	samples, err := diskCollector("testdata/mounts.txt", []string{"/"})
	if err != nil {
		t.Fatal(err)
	}

	values := metricValues(samples)
	for _, name := range []string{
		"disk.root.total",
		"disk.root.used",
		"disk.root.free",
		"disk.root.used_percent",
	} {
		if _, ok := values[name]; !ok {
			t.Fatalf("missing metric %s in %+v", name, samples)
		}
	}

	for _, sample := range samples {
		if sample.Collector != "disk" {
			t.Fatalf("%s collector = %q, want disk", sample.Name, sample.Collector)
		}
		if sample.Timestamp.IsZero() {
			t.Fatalf("%s timestamp was not set", sample.Name)
		}
	}
}

func TestDiskCollectorSkipsMissingMount(t *testing.T) {
	samples, err := diskCollector("testdata/mounts.txt", []string{"/definitely-not-mounted"})
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 0 {
		t.Fatalf("expected no samples for missing mount, got %+v", samples)
	}
}

func TestMountPointToName(t *testing.T) {
	tests := map[string]string{
		"/":        "root",
		"/home":    "home",
		"/var/log": "var_log",
	}

	for mount, want := range tests {
		if got := mountPointToName(mount); got != want {
			t.Fatalf("mountPointToName(%q) = %q, want %q", mount, got, want)
		}
	}
}

func TestDiskFillPredictorReportsGrowthRisk(t *testing.T) {
	predictor := newDiskFillPredictor()
	first := []MetricSample{
		{Name: "disk.root.total", Value: 100, Timestamp: time.Unix(0, 0), Collector: "disk"},
		{Name: "disk.root.used", Value: 50, Timestamp: time.Unix(0, 0), Collector: "disk"},
	}
	second := []MetricSample{
		{Name: "disk.root.total", Value: 100, Timestamp: time.Unix(60, 0), Collector: "disk"},
		{Name: "disk.root.used", Value: 75, Timestamp: time.Unix(60, 0), Collector: "disk"},
	}

	values := metricValues(predictor.collect(first, 2*time.Minute))
	if values["disk.root.fills_within_window"] != 0 {
		t.Fatalf("first sample should not predict fill risk: %+v", values)
	}

	values = metricValues(predictor.collect(second, 2*time.Minute))
	if values["disk.root.fills_within_window"] != 1 {
		t.Fatalf("growing disk should predict fill risk: %+v", values)
	}
}

func TestDiskFillPredictorIgnoresShrinkingDisk(t *testing.T) {
	predictor := newDiskFillPredictor()
	predictor.collect([]MetricSample{
		{Name: "disk.root.total", Value: 100, Timestamp: time.Unix(0, 0), Collector: "disk"},
		{Name: "disk.root.used", Value: 75, Timestamp: time.Unix(0, 0), Collector: "disk"},
	}, time.Hour)

	values := metricValues(predictor.collect([]MetricSample{
		{Name: "disk.root.total", Value: 100, Timestamp: time.Unix(60, 0), Collector: "disk"},
		{Name: "disk.root.used", Value: 50, Timestamp: time.Unix(60, 0), Collector: "disk"},
	}, time.Hour))
	if values["disk.root.fills_within_window"] != 0 {
		t.Fatalf("shrinking disk should not predict fill risk: %+v", values)
	}
}
