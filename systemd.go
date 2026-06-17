package main

import (
	"os/exec"
	"strings"
	"time"
)

var runSystemctl = func(service string) error {
	return exec.Command("systemctl", "is-active", "--quiet", service).Run()
}

func systemdCollector(services []string) []MetricSample {
	collectedAt := time.Now()
	samples := make([]MetricSample, 0, len(services))
	for _, service := range services {
		service = strings.TrimSpace(service)
		if service == "" {
			continue
		}
		samples = append(samples, stateSample("systemd", service, ".unhealthy", runSystemctl(service) != nil, collectedAt))
	}
	return samples
}
