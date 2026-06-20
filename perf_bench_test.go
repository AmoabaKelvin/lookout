package main

import (
	"testing"
	"time"
)

func BenchmarkMetricsRender(b *testing.B) {
	snapshot := newMetricsSnapshot("bench-host", "bench")
	now := time.Unix(1700000000, 0)
	snapshot.Update([]MetricSample{
		{Name: "memory.used_percent", Value: 73.4, Timestamp: now, Collector: "memory"},
		{Name: "memory.total", Value: 8192000, Timestamp: now, Collector: "memory"},
		{Name: "memory.used", Value: 4096000, Timestamp: now, Collector: "memory"},
		{Name: "memory.available", Value: 4096000, Timestamp: now, Collector: "memory"},
		{Name: "swap.used_percent", Value: 0, Timestamp: now, Collector: "swap"},
		{Name: "cpu.used_percent", Value: 18.2, Timestamp: now, Collector: "cpu"},
		{Name: "load.1", Value: 0.42, Timestamp: now, Collector: "load"},
		{Name: "load.1_per_core", Value: 0.10, Timestamp: now, Collector: "load"},
		{Name: "disk.root.total", Value: 1000000, Timestamp: now, Collector: "disk"},
		{Name: "disk.root.used", Value: 610000, Timestamp: now, Collector: "disk"},
		{Name: "disk.root.free", Value: 390000, Timestamp: now, Collector: "disk"},
		{Name: "disk.root.used_percent", Value: 61, Timestamp: now, Collector: "disk"},
		{Name: "systemd.nginx.unhealthy", Value: 0, Timestamp: now, Collector: "systemd"},
		{Name: "http.app.unhealthy", Value: 0, Timestamp: now, Collector: "http"},
		{Name: "tcp.redis.unhealthy", Value: 0, Timestamp: now, Collector: "tcp"},
		{Name: "process.nginx.missing", Value: 0, Timestamp: now, Collector: "process"},
	})

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = snapshot.Render()
	}
}

func BenchmarkAlertEvaluate(b *testing.B) {
	cfg := defaultConfig()
	cfg.validate()
	am := NewAlertManager(buildRules(cfg), time.Hour, "bench-host", nil)
	sample := MetricSample{Name: "memory.used_percent", Value: 20, Unit: "percent", Collector: "memory"}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		am.Evaluate(sample)
	}
}

func BenchmarkMemoryCollectorFixture(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := memoryCollector("testdata/meminfo.txt"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseCPUTimes(b *testing.B) {
	data := []byte("cpu  4705 0 2254 1362393 345 0 123 0 0 0\n")

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := parseCPUTimes(data, "bench"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFormatAlert(b *testing.B) {
	alert := Alert{
		IsFiring:  true,
		Title:     "High memory usage",
		Value:     90,
		Threshold: 85,
		Hostname:  "bench-host",
		Metric:    "memory.used_percent",
		Unit:      "percent",
		Severity:  SeverityWarning,
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = formatAlert(alert)
	}
}

func BenchmarkProcessCollectorEmptyConfig(b *testing.B) {
	source := b.TempDir()

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = processCollector(source, nil)
	}
}
