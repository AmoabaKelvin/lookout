package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPCollectorReportsHealthyAndUnhealthy(t *testing.T) {
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer okServer.Close()

	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failServer.Close()

	samples := httpCollector([]HTTPCheckConfig{
		{Name: "api", URL: okServer.URL, Timeout: Duration(time.Second), ExpectedStatus: http.StatusNoContent},
		{Name: "bad api", URL: failServer.URL, Timeout: Duration(time.Second), ExpectedStatus: http.StatusNoContent},
	})

	values := metricValues(samples)
	if values["http.api.unhealthy"] != 0 {
		t.Fatalf("healthy http check reported unhealthy: %+v", samples)
	}
	if values["http.bad_api.unhealthy"] != 1 {
		t.Fatalf("unhealthy http check did not report unhealthy: %+v", samples)
	}
}

func TestHTTPCollectorReportsInvalidURLUnhealthy(t *testing.T) {
	samples := httpCollector([]HTTPCheckConfig{{Name: "bad", URL: "not-a-url"}})
	values := metricValues(samples)
	if values["http.bad.unhealthy"] != 1 {
		t.Fatalf("invalid URL should be unhealthy: %+v", samples)
	}
}
