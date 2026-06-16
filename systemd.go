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
		value := float64(0)
		if err := runSystemctl(service); err != nil {
			value = 1
		}
		samples = append(samples, MetricSample{
			Name:      "systemd." + safeMetricPart(service) + ".unhealthy",
			Value:     value,
			Unit:      "state",
			Timestamp: collectedAt,
			Collector: "systemd",
		})
	}
	return samples
}
