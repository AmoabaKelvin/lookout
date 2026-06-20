package main

import (
	"strings"
	"testing"
)

// richConfig is a validated config that exercises every rule family: threshold
// rules, multiple disk mounts, disk-fill prediction, and each name-derived check.
func richConfig() Config {
	cfg := defaultConfig()
	cfg.Alerts.Disk.Mounts = []string{"/", "/var"}
	cfg.Alerts.Systemd.Services = []string{"nginx"}
	cfg.Alerts.HTTP.Checks = []HTTPCheckConfig{{Name: "app", URL: "https://example.com/health"}}
	cfg.Alerts.TCP.Checks = []TCPCheckConfig{{Address: "127.0.0.1:6379"}} // no name -> address fallback
	cfg.Alerts.Process.Names = []string{"postgres"}
	cfg.validate() // populates ResolveBelow pointers and check defaults
	return cfg
}

// sampleFor reconstructs the metric sample a collector would emit for a metric
// name, so a rule's matcher can be exercised against it. The collector is the
// first dotted segment, which matches how the collectors tag their samples.
func sampleFor(name string) MetricSample {
	return MetricSample{Name: name, Collector: strings.SplitN(name, ".", 2)[0]}
}

// TestTrackedMetricsMatchRules locks in the invariant that buildRuleSpecs gives
// buildRules and trackedMetrics: every tracked metric must reference a real rule
// whose matcher accepts it. If the two ever drift, staleness tracking silently
// breaks, so this guards the single source of truth.
func TestTrackedMetricsMatchRules(t *testing.T) {
	cfg := richConfig()

	rulesByID := make(map[string]Rule)
	for _, r := range buildRules(cfg) {
		rulesByID[r.ID] = r
	}

	tracked := trackedMetrics(cfg)
	if len(tracked) == 0 {
		t.Fatal("expected tracked metrics, got none")
	}

	for _, tm := range tracked {
		rule, ok := rulesByID[tm.RuleID]
		if !ok {
			t.Errorf("tracked metric %q references rule %q that buildRules does not produce", tm.Name, tm.RuleID)
			continue
		}
		if !rule.Matcher(sampleFor(tm.Name)) {
			t.Errorf("rule %q does not match its tracked metric %q", tm.RuleID, tm.Name)
		}
	}
}

// TestTrackedMetricsCoverage spot-checks that the expected metric names are
// derived for a representative config, guarding the name construction itself.
func TestTrackedMetricsCoverage(t *testing.T) {
	cfg := richConfig()

	got := make(map[string]string) // name -> ruleID
	for _, tm := range trackedMetrics(cfg) {
		got[tm.Name] = tm.RuleID
	}

	want := map[string]string{
		"memory.used_percent":           "memory",
		"disk.root.used_percent":        "disk",
		"disk.var.used_percent":         "disk",
		"disk.root.fills_within_window": "disk-fill",
		"disk.var.fills_within_window":  "disk-fill",
		"load.1_per_core":               "load",
		"cpu.used_percent":              "cpu",
		"swap.used_percent":             "swap",
		"systemd.nginx.unhealthy":       "systemd-nginx",
		"http.app.unhealthy":            "http-app",
		"tcp.127_0_0_1_6379.unhealthy":  "tcp-127_0_0_1_6379",
		"process.postgres.missing":      "process-postgres",
	}

	for name, ruleID := range want {
		if got[name] != ruleID {
			t.Errorf("tracked metric %q: got rule %q, want %q", name, got[name], ruleID)
		}
	}
	if len(got) != len(want) {
		t.Errorf("tracked metric count = %d, want %d (got: %v)", len(got), len(want), got)
	}
}

// TestDiskFillRulesOmittedWhenPredictionDisabled confirms disk-fill rules and
// their tracked metrics drop out together when prediction is off.
func TestDiskFillRulesOmittedWhenPredictionDisabled(t *testing.T) {
	cfg := richConfig()
	cfg.Alerts.Disk.PredictFullWithin = 0

	for _, r := range buildRules(cfg) {
		if r.ID == "disk-fill" {
			t.Fatal("disk-fill rule present despite prediction disabled")
		}
	}
	for _, tm := range trackedMetrics(cfg) {
		if tm.RuleID == "disk-fill" {
			t.Fatalf("disk-fill tracked metric %q present despite prediction disabled", tm.Name)
		}
	}
}
