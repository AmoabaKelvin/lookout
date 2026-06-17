package main

import (
	"errors"
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
		samples = append(samples, stateSample("http", name, ".unhealthy", runHTTPCheck(check) != nil, collectedAt))
	}
	return samples
}

func runHTTPCheck(check HTTPCheckConfig) error {
	if check.URL == "" {
		return errors.New("missing url")
	}
	parsed, err := url.Parse(check.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("invalid url")
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
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection can be reused
	if resp.StatusCode != expectedStatus {
		return fmt.Errorf("status %d, want %d", resp.StatusCode, expectedStatus)
	}
	return nil
}
