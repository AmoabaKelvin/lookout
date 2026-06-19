package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

type MetricSample struct {
	Name      string
	Value     float64
	Unit      string
	Timestamp time.Time
	Collector string
}

// stateSample builds a 0/1 health gauge for a named target (systemd unit, TCP/HTTP
// check, process). Value is 1 when unhealthy/missing, 0 otherwise.
func stateSample(collector, name, suffix string, unhealthy bool, at time.Time) MetricSample {
	var value float64
	if unhealthy {
		value = 1
	}
	return MetricSample{
		Name:      collector + "." + safeMetricPart(name) + suffix,
		Value:     value,
		Unit:      "state",
		Timestamp: at,
		Collector: collector,
	}
}

func safeMetricPart(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

type metricsSnapshot struct {
	mu             sync.RWMutex
	host           string
	version        string
	lastCollection time.Time
	samples        map[string]MetricSample
}

func newMetricsSnapshot(host, version string) *metricsSnapshot {
	return &metricsSnapshot{host: host, version: version, samples: make(map[string]MetricSample)}
}

func (s *metricsSnapshot) Update(samples []MetricSample) {
	if len(samples) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastCollection = time.Now()
	for _, sample := range samples {
		s.samples[sample.Name] = sample
	}
}

func (s *metricsSnapshot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/metrics" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(s.Render()))
}

func (s *metricsSnapshot) Render() string {
	s.mu.RLock()
	samples := make([]MetricSample, 0, len(s.samples))
	for _, sample := range s.samples {
		samples = append(samples, sample)
	}
	lastCollection := s.lastCollection
	s.mu.RUnlock()

	sort.Slice(samples, func(i, j int) bool {
		return samples[i].Name < samples[j].Name
	})

	var b strings.Builder
	b.Grow(256 + len(samples)*128)
	emittedTypes := make(map[string]bool)
	s.renderSelfMetrics(&b, emittedTypes, lastCollection)
	for _, sample := range samples {
		name, labels := s.prometheusMetric(sample)
		if name == "" {
			continue
		}
		writePrometheusSample(&b, emittedTypes, name, labels, sample.Value, sample.Timestamp)
	}
	return b.String()
}

func (s *metricsSnapshot) renderSelfMetrics(b *strings.Builder, emittedTypes map[string]bool, lastCollection time.Time) {
	writePrometheusSample(b, emittedTypes, "lookout_up", prometheusLabels("host", s.host), 1, time.Time{})
	writePrometheusSample(b, emittedTypes, "lookout_build_info", prometheusLabels("host", s.host, "version", s.version), 1, time.Time{})

	var lastCollectionSeconds float64
	if !lastCollection.IsZero() {
		lastCollectionSeconds = float64(lastCollection.Unix())
	}
	writePrometheusSample(b, emittedTypes, "lookout_last_collection_timestamp_seconds", prometheusLabels("host", s.host), lastCollectionSeconds, time.Time{})
}

func startMetricsServer(ctx context.Context, cfg MetricsConfig, snapshot *metricsSnapshot) error {
	if !cfg.Enabled {
		return nil
	}

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}

	server := &http.Server{
		Handler:           snapshot,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("metrics: shutdown: %v", err)
		}
	}()
	go func() {
		log.Printf("metrics: serving Prometheus endpoint on http://%s/metrics", listener.Addr())
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("metrics: server stopped: %v", err)
		}
	}()
	return nil
}

func (s *metricsSnapshot) prometheusMetric(sample MetricSample) (string, string) {
	switch {
	case sample.Collector == "disk":
		mount, field, ok := splitDiskMetric(sample.Name)
		if !ok {
			break
		}
		return safePrometheusName("disk_" + field), prometheusLabels("host", s.host, "mount", mount)

	case isStateCollector(sample.Collector):
		target, field, ok := splitStateMetric(sample)
		if !ok {
			break
		}
		return safePrometheusName(sample.Collector + "_" + field), prometheusLabels("host", s.host, "name", target)

	default:
		return safePrometheusName(sample.Name), prometheusLabels("host", s.host)
	}
	return "", ""
}

func writePrometheusSample(b *strings.Builder, emittedTypes map[string]bool, name string, labels string, value float64, timestamp time.Time) {
	if !emittedTypes[name] {
		b.WriteString("# TYPE ")
		b.WriteString(name)
		b.WriteString(" gauge\n")
		emittedTypes[name] = true
	}
	b.WriteString(name)
	b.WriteString(labels)
	b.WriteByte(' ')
	var buf [64]byte
	b.Write(strconv.AppendFloat(buf[:0], value, 'f', -1, 64))
	if !timestamp.IsZero() {
		b.WriteByte(' ')
		b.Write(strconv.AppendInt(buf[:0], timestamp.UnixMilli(), 10))
	}
	b.WriteByte('\n')
}

func isStateCollector(collector string) bool {
	switch collector {
	case "systemd", "http", "tcp", "process":
		return true
	default:
		return false
	}
}

func splitStateMetric(sample MetricSample) (string, string, bool) {
	prefix := sample.Collector + "."
	if !strings.HasPrefix(sample.Name, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(sample.Name, prefix)
	i := strings.LastIndexByte(rest, '.')
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

func safePrometheusName(value string) string {
	var b strings.Builder
	for i, r := range value {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isDigit := r >= '0' && r <= '9'
		if isLetter || r == '_' || (isDigit && i > 0) {
			b.WriteRune(r)
			continue
		}
		if isDigit && i == 0 {
			b.WriteByte('_')
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	if b.Len() == 0 {
		return "lookout_metric"
	}
	return b.String()
}

func prometheusLabels(pairs ...string) string {
	if len(pairs) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(pairs[i])
		b.WriteString("=\"")
		b.WriteString(escapePrometheusLabel(pairs[i+1]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func escapePrometheusLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, "\"", "\\\"")
}
