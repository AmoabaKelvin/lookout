package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func httpCollector(checks []HTTPCheckConfig) []MetricSample {
	collectedAt := time.Now()
	samples := make([]MetricSample, 0, len(checks))

	for _, check := range checks {
		name := strings.TrimSpace(check.Name)
		if name == "" {
			name = check.URL
		}
		value := float64(0)
		if err := runHTTPCheck(check); err != nil {
			value = 1
		}
		samples = append(samples, MetricSample{
			Name:      "http." + safeMetricPart(name) + ".unhealthy",
			Value:     value,
			Unit:      "state",
			Timestamp: collectedAt,
			Collector: "http",
		})
	}
	return samples
}

func runHTTPCheck(check HTTPCheckConfig) error {
	if check.URL == "" {
		return fmt.Errorf("missing url")
	}
	parsed, err := url.Parse(check.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("invalid url")
	}

	timeout := check.Timeout.Std()
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	expectedStatus := check.ExpectedStatus
	if expectedStatus == 0 {
		expectedStatus = http.StatusOK
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(check.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != expectedStatus {
		return fmt.Errorf("status %d, want %d", resp.StatusCode, expectedStatus)
	}
	return nil
}
