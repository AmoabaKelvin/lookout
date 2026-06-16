package main

import "testing"

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
