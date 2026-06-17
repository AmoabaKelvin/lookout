package main

import (
	"errors"
	"net"
	"strings"
	"time"
)

func tcpCollector(checks []TCPCheckConfig) []MetricSample {
	collectedAt := time.Now()
	samples := make([]MetricSample, 0, len(checks))

	for _, check := range checks {
		name := strings.TrimSpace(check.Name)
		if name == "" {
			name = check.Address
		}
		if strings.TrimSpace(name) == "" {
			continue
		}

		samples = append(samples, stateSample("tcp", name, ".unhealthy", runTCPCheck(check) != nil, collectedAt))
	}
	return samples
}

func runTCPCheck(check TCPCheckConfig) error {
	if strings.TrimSpace(check.Address) == "" {
		return errors.New("missing address")
	}
	timeout := check.Timeout.Std()
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	conn, err := net.DialTimeout("tcp", check.Address, timeout)
	if err != nil {
		return err
	}
	return conn.Close()
}
