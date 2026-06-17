package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProcessCollectorReportsPresentAndMissingProcesses(t *testing.T) {
	proc := t.TempDir()
	writeProcess(t, proc, "100", "nginx\n", "/usr/sbin/nginx\x00-g\x00daemon off\x00")
	writeProcess(t, proc, "101", "postgres\n", "")
	if err := os.Mkdir(filepath.Join(proc, "not-a-pid"), 0o700); err != nil {
		t.Fatal(err)
	}

	samples := processCollector(proc, []string{"nginx", "postgres", "redis"})
	values := metricValues(samples)

	if values["process.nginx.missing"] != 0 {
		t.Fatalf("present comm process reported missing: %+v", samples)
	}
	if values["process.postgres.missing"] != 0 {
		t.Fatalf("present comm-only process reported missing: %+v", samples)
	}
	if values["process.redis.missing"] != 1 {
		t.Fatalf("missing process did not report missing: %+v", samples)
	}
}

func writeProcess(t *testing.T, proc string, pid string, comm string, cmdline string) {
	t.Helper()
	dir := filepath.Join(proc, pid)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o600); err != nil {
		t.Fatal(err)
	}
}
