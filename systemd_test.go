package main

import "testing"

func TestSystemdCollectorReportsServiceState(t *testing.T) {
	original := runSystemctl
	t.Cleanup(func() { runSystemctl = original })

	runSystemctl = func(service string) error {
		if service == "nginx" {
			return nil
		}
		return errSystemdInactive{}
	}

	samples := systemdCollector([]string{"nginx", "bad service"})
	values := metricValues(samples)

	if values["systemd.nginx.unhealthy"] != 0 {
		t.Fatalf("healthy service reported unhealthy: %+v", samples)
	}
	if values["systemd.bad_service.unhealthy"] != 1 {
		t.Fatalf("unhealthy service did not report unhealthy: %+v", samples)
	}
}

type errSystemdInactive struct{}

func (errSystemdInactive) Error() string { return "inactive" }
