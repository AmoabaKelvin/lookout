package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsSnapshotRendersPrometheusText(t *testing.T) {
	snapshot := newMetricsSnapshot("web-1", "v1.2.3")
	snapshot.Update([]MetricSample{
		{Name: "memory.used_percent", Value: 73.4, Timestamp: time.Unix(1700000000, 123000000), Collector: "memory"},
		{Name: "disk.root.used_percent", Value: 61.2, Timestamp: time.Unix(1700000001, 0), Collector: "disk"},
		{Name: "systemd.nginx.unhealthy", Value: 1, Timestamp: time.Unix(1700000002, 0), Collector: "systemd"},
	})

	rendered := snapshot.Render()
	for _, want := range []string{
		"# TYPE lookout_up gauge\n",
		"lookout_up{host=\"web-1\"} 1\n",
		"# TYPE lookout_build_info gauge\n",
		"lookout_build_info{host=\"web-1\",version=\"v1.2.3\"} 1\n",
		"# TYPE lookout_last_collection_timestamp_seconds gauge\n",
		"# TYPE disk_used_percent gauge\n",
		"disk_used_percent{host=\"web-1\",mount=\"root\"} 61.2 1700000001000\n",
		"# TYPE memory_used_percent gauge\n",
		"memory_used_percent{host=\"web-1\"} 73.4 1700000000123\n",
		"# TYPE systemd_unhealthy gauge\n",
		"systemd_unhealthy{host=\"web-1\",name=\"nginx\"} 1 1700000002000\n",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered metrics missing %q:\n%s", want, rendered)
		}
	}
}

func TestMetricsSnapshotRendersSelfMetricsBeforeCollection(t *testing.T) {
	snapshot := newMetricsSnapshot("web-1", "dev")

	rendered := snapshot.Render()
	for _, want := range []string{
		"lookout_up{host=\"web-1\"} 1\n",
		"lookout_build_info{host=\"web-1\",version=\"dev\"} 1\n",
		"lookout_last_collection_timestamp_seconds{host=\"web-1\"} 0\n",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered metrics missing %q:\n%s", want, rendered)
		}
	}
}

func TestMetricsSnapshotServesOnlyMetricsEndpoint(t *testing.T) {
	snapshot := newMetricsSnapshot("web-1", "dev")
	snapshot.Update([]MetricSample{{Name: "cpu.used_percent", Value: 18, Collector: "cpu"}})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	snapshot.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain; version=0.0.4") {
		t.Fatalf("content-type = %q", got)
	}
	if !strings.Contains(rec.Body.String(), "cpu_used_percent{host=\"web-1\"} 18\n") {
		t.Fatalf("body missing cpu metric:\n%s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	rec = httptest.NewRecorder()
	snapshot.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /health status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
