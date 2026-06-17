package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigDefaults(t *testing.T) {
	// An empty file should leave every default in place.
	cfg, err := LoadConfig(writeConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CollectionInterval.Std() != 30*time.Second {
		t.Errorf("collection interval: got %s", cfg.CollectionInterval.Std())
	}
	if cfg.StateFile != defaultStateFile {
		t.Errorf("state file: got %s", cfg.StateFile)
	}
	if cfg.Alerts.Memory.Threshold != 85 {
		t.Errorf("memory threshold: got %v", cfg.Alerts.Memory.Threshold)
	}
	if cfg.Alerts.StaleAfter.Std() != 90*time.Second {
		t.Errorf("stale after: got %s", cfg.Alerts.StaleAfter.Std())
	}
	if cfg.Alerts.Memory.ResolveBelow == nil || *cfg.Alerts.Memory.ResolveBelow != 80 {
		t.Errorf("memory resolve below: got %v", cfg.Alerts.Memory.ResolveBelow)
	}
	if cfg.Alerts.Disk.ResolveBelow == nil || *cfg.Alerts.Disk.ResolveBelow != 80 {
		t.Errorf("disk resolve below: got %v", cfg.Alerts.Disk.ResolveBelow)
	}
	if cfg.Alerts.Disk.For.Std() != 2*time.Minute {
		t.Errorf("disk for: got %s", cfg.Alerts.Disk.For.Std())
	}
	if cfg.Alerts.Load.Threshold != 1.5 {
		t.Errorf("load threshold: got %v", cfg.Alerts.Load.Threshold)
	}
	if cfg.Alerts.Load.ResolveBelow == nil || *cfg.Alerts.Load.ResolveBelow != 1 {
		t.Errorf("load resolve below: got %v", cfg.Alerts.Load.ResolveBelow)
	}
	if cfg.Alerts.Disk.PredictFullWithin.Std() != 4*time.Hour {
		t.Errorf("disk predict_full_within: got %s", cfg.Alerts.Disk.PredictFullWithin.Std())
	}
	if cfg.Alerts.CPU.Threshold != 85 {
		t.Errorf("cpu threshold: got %v", cfg.Alerts.CPU.Threshold)
	}
	if cfg.Alerts.CPU.ResolveBelow == nil || *cfg.Alerts.CPU.ResolveBelow != 80 {
		t.Errorf("cpu resolve below: got %v", cfg.Alerts.CPU.ResolveBelow)
	}
	if cfg.Alerts.Swap.Threshold != 80 {
		t.Errorf("swap threshold: got %v", cfg.Alerts.Swap.Threshold)
	}
	if cfg.Alerts.Swap.ResolveBelow == nil || *cfg.Alerts.Swap.ResolveBelow != 75 {
		t.Errorf("swap resolve below: got %v", cfg.Alerts.Swap.ResolveBelow)
	}
	if cfg.Alerts.Systemd.Severity != SeverityCritical {
		t.Errorf("systemd severity: got %s", cfg.Alerts.Systemd.Severity)
	}
	if cfg.Alerts.HTTP.Severity != SeverityCritical {
		t.Errorf("http severity: got %s", cfg.Alerts.HTTP.Severity)
	}
	if cfg.Alerts.TCP.Severity != SeverityCritical {
		t.Errorf("tcp severity: got %s", cfg.Alerts.TCP.Severity)
	}
	if cfg.Alerts.Process.Severity != SeverityCritical {
		t.Errorf("process defaults: %+v", cfg.Alerts.Process)
	}
	if cfg.Docker.Severity != SeverityCritical {
		t.Errorf("docker severity: got %s", cfg.Docker.Severity)
	}
	if cfg.Docker.RestartThreshold != 3 || cfg.Docker.RestartWindow.Std() != 10*time.Minute {
		t.Errorf("docker restart defaults: %+v", cfg.Docker)
	}
	if len(cfg.Alerts.Disk.Mounts) == 0 {
		t.Errorf("expected default mounts")
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
collection_interval: 1m
state_file: /tmp/lookout-state.json
alerts:
  stale_after: 4m
  memory:
    threshold: 90
    resolve_below: 82
    for: 30s
    severity: warning
  disk:
    threshold: 70
    resolve_below: 65
    predict_full_within: 8h
    mounts:
      - /
      - /data
  load:
    threshold: 8
    resolve_below: 6
    for: 1m
    severity: critical
  cpu:
    threshold: 75
    resolve_below: 65
    for: 45s
    severity: critical
  swap:
    threshold: 60
    resolve_below: 50
    for: 1m
    severity: critical
  systemd:
    services:
      - nginx
    severity: warning
  http:
    severity: warning
    checks:
      - name: app
        url: "https://example.com/health"
        timeout: 3s
        expected_status: 204
  tcp:
    severity: warning
    checks:
      - name: redis
        address: "127.0.0.1:6379"
        timeout: 2s
  process:
    severity: warning
    names:
      - nginx
notifiers:
  slack:
    webhook_url: "https://hooks.slack.com/services/X/Y/Z"
  teams:
    webhook_url: "https://example.webhook.office.com/team"
  pagerduty:
    integration_key: "pagerduty-key"
heartbeat:
  url: "https://hc-ping.com/abc"
  interval: 45s
docker:
  enabled: true
  severity: warning
  restart_threshold: 5
  restart_window: 3m
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CollectionInterval.Std() != time.Minute {
		t.Errorf("collection interval: got %s", cfg.CollectionInterval.Std())
	}
	if cfg.StateFile != "/tmp/lookout-state.json" {
		t.Errorf("state file override: got %s", cfg.StateFile)
	}
	if cfg.Alerts.Memory.Threshold != 90 || cfg.Alerts.Memory.For.Std() != 30*time.Second {
		t.Errorf("memory overrides not applied: %+v", cfg.Alerts.Memory)
	}
	if cfg.Alerts.StaleAfter.Std() != 4*time.Minute {
		t.Errorf("stale_after override: got %s", cfg.Alerts.StaleAfter.Std())
	}
	if cfg.Alerts.Memory.ResolveBelow == nil || *cfg.Alerts.Memory.ResolveBelow != 82 {
		t.Errorf("memory resolve below override: got %v", cfg.Alerts.Memory.ResolveBelow)
	}
	if cfg.Alerts.Memory.Severity != SeverityWarning {
		t.Errorf("severity override: got %s", cfg.Alerts.Memory.Severity)
	}
	if cfg.Alerts.Disk.Threshold != 70 {
		t.Errorf("disk threshold override: got %v", cfg.Alerts.Disk.Threshold)
	}
	if cfg.Alerts.Disk.ResolveBelow == nil || *cfg.Alerts.Disk.ResolveBelow != 65 {
		t.Errorf("disk resolve below override: got %v", cfg.Alerts.Disk.ResolveBelow)
	}
	if got := cfg.Alerts.Disk.Mounts; len(got) != 2 || got[1] != "/data" {
		t.Errorf("mounts override: got %v", got)
	}
	if cfg.Alerts.Disk.PredictFullWithin.Std() != 8*time.Hour {
		t.Errorf("disk predict_full_within override: got %s", cfg.Alerts.Disk.PredictFullWithin.Std())
	}
	if cfg.Alerts.Load.Threshold != 8 || cfg.Alerts.Load.For.Std() != time.Minute {
		t.Errorf("load overrides not applied: %+v", cfg.Alerts.Load)
	}
	if cfg.Alerts.Load.ResolveBelow == nil || *cfg.Alerts.Load.ResolveBelow != 6 {
		t.Errorf("load resolve below override: got %v", cfg.Alerts.Load.ResolveBelow)
	}
	if cfg.Alerts.Load.Severity != SeverityCritical {
		t.Errorf("load severity override: got %s", cfg.Alerts.Load.Severity)
	}
	if cfg.Alerts.CPU.Threshold != 75 || cfg.Alerts.CPU.For.Std() != 45*time.Second {
		t.Errorf("cpu overrides not applied: %+v", cfg.Alerts.CPU)
	}
	if cfg.Alerts.CPU.ResolveBelow == nil || *cfg.Alerts.CPU.ResolveBelow != 65 {
		t.Errorf("cpu resolve below override: got %v", cfg.Alerts.CPU.ResolveBelow)
	}
	if cfg.Alerts.CPU.Severity != SeverityCritical {
		t.Errorf("cpu severity override: got %s", cfg.Alerts.CPU.Severity)
	}
	if cfg.Alerts.Swap.Threshold != 60 || cfg.Alerts.Swap.For.Std() != time.Minute {
		t.Errorf("swap overrides not applied: %+v", cfg.Alerts.Swap)
	}
	if cfg.Alerts.Swap.ResolveBelow == nil || *cfg.Alerts.Swap.ResolveBelow != 50 {
		t.Errorf("swap resolve below override: got %v", cfg.Alerts.Swap.ResolveBelow)
	}
	if cfg.Alerts.Swap.Severity != SeverityCritical {
		t.Errorf("swap severity override: got %s", cfg.Alerts.Swap.Severity)
	}
	if len(cfg.Alerts.Systemd.Services) != 1 || cfg.Alerts.Systemd.Services[0] != "nginx" || cfg.Alerts.Systemd.Severity != SeverityWarning {
		t.Errorf("systemd overrides not applied: %+v", cfg.Alerts.Systemd)
	}
	if len(cfg.Alerts.HTTP.Checks) != 1 || cfg.Alerts.HTTP.Checks[0].Name != "app" || cfg.Alerts.HTTP.Checks[0].ExpectedStatus != 204 || cfg.Alerts.HTTP.Checks[0].Timeout.Std() != 3*time.Second {
		t.Errorf("http overrides not applied: %+v", cfg.Alerts.HTTP)
	}
	if len(cfg.Alerts.TCP.Checks) != 1 || cfg.Alerts.TCP.Checks[0].Name != "redis" || cfg.Alerts.TCP.Checks[0].Address != "127.0.0.1:6379" || cfg.Alerts.TCP.Checks[0].Timeout.Std() != 2*time.Second || cfg.Alerts.TCP.Severity != SeverityWarning {
		t.Errorf("tcp overrides not applied: %+v", cfg.Alerts.TCP)
	}
	if len(cfg.Alerts.Process.Names) != 1 || cfg.Alerts.Process.Names[0] != "nginx" || cfg.Alerts.Process.Severity != SeverityWarning {
		t.Errorf("process overrides not applied: %+v", cfg.Alerts.Process)
	}
	if cfg.Notifiers.Slack == nil || cfg.Notifiers.Slack.WebhookURL == "" {
		t.Errorf("slack notifier not parsed")
	}
	if cfg.Notifiers.Teams == nil || cfg.Notifiers.Teams.WebhookURL == "" {
		t.Errorf("teams notifier not parsed")
	}
	if cfg.Notifiers.PagerDuty == nil || cfg.Notifiers.PagerDuty.IntegrationKey != "pagerduty-key" {
		t.Errorf("pagerduty notifier not parsed: %+v", cfg.Notifiers.PagerDuty)
	}
	if cfg.Notifiers.Discord != nil {
		t.Errorf("absent notifier should be nil, got %+v", cfg.Notifiers.Discord)
	}
	if !cfg.Docker.Enabled || cfg.Docker.Severity != SeverityWarning || cfg.Docker.RestartThreshold != 5 || cfg.Docker.RestartWindow.Std() != 3*time.Minute {
		t.Errorf("docker overrides not applied: %+v", cfg.Docker)
	}
}

func TestLoadConfigMemoryThresholdForFiveMinutes(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
alerts:
  memory:
    threshold: 80
    for: 5m
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Alerts.Memory.Threshold != 80 {
		t.Fatalf("memory threshold: got %v", cfg.Alerts.Memory.Threshold)
	}
	if cfg.Alerts.Memory.For.Std() != 5*time.Minute {
		t.Fatalf("memory for duration: got %s", cfg.Alerts.Memory.For.Std())
	}
	if cfg.Alerts.Memory.ResolveBelow == nil || *cfg.Alerts.Memory.ResolveBelow != 75 {
		t.Fatalf("memory resolve below default: got %v", cfg.Alerts.Memory.ResolveBelow)
	}
}

func TestLoadConfigStaleAfterDefaultsToThreeCollectionIntervals(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
collection_interval: 1m
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Alerts.StaleAfter.Std() != 3*time.Minute {
		t.Fatalf("stale_after default: got %s", cfg.Alerts.StaleAfter.Std())
	}
}

func TestLoadConfigInvalidDuration(t *testing.T) {
	_, err := LoadConfig(writeConfig(t, "collection_interval: notaduration\n"))
	if err == nil {
		t.Fatal("expected an error for an invalid duration string")
	}
}

func TestLoadConfigClampsOutOfRange(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
collection_interval: 0s
alerts:
  memory:
    threshold: 250
  disk:
    severity: bogus
  load:
    threshold: 0
    severity: bogus
  cpu:
    threshold: -1
    severity: bogus
  swap:
    threshold: 250
    severity: bogus
  systemd:
    severity: bogus
  http:
    severity: bogus
    checks:
      - name: app
        url: "https://example.com"
  tcp:
    severity: bogus
    checks:
      - name: redis
        address: "127.0.0.1:6379"
  process:
    severity: bogus
docker:
  severity: bogus
  restart_threshold: 0
  restart_window: 0s
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CollectionInterval.Std() != 30*time.Second {
		t.Errorf("non-positive interval should clamp to default, got %s", cfg.CollectionInterval.Std())
	}
	if cfg.Alerts.Memory.Threshold != 100 {
		t.Errorf("threshold >100 should clamp to 100, got %v", cfg.Alerts.Memory.Threshold)
	}
	if cfg.Alerts.Disk.Severity != SeverityWarning {
		t.Errorf("invalid severity should fall back, got %s", cfg.Alerts.Disk.Severity)
	}
	if cfg.Alerts.Load.Threshold != 1.5 {
		t.Errorf("non-positive load threshold should fall back, got %v", cfg.Alerts.Load.Threshold)
	}
	if cfg.Alerts.Load.Severity != SeverityWarning {
		t.Errorf("invalid load severity should fall back, got %s", cfg.Alerts.Load.Severity)
	}
	if cfg.Alerts.CPU.Threshold != 0 {
		t.Errorf("negative cpu threshold should clamp to 0, got %v", cfg.Alerts.CPU.Threshold)
	}
	if cfg.Alerts.CPU.Severity != SeverityWarning {
		t.Errorf("invalid cpu severity should fall back, got %s", cfg.Alerts.CPU.Severity)
	}
	if cfg.Alerts.Swap.Threshold != 100 {
		t.Errorf("swap threshold >100 should clamp to 100, got %v", cfg.Alerts.Swap.Threshold)
	}
	if cfg.Alerts.Swap.Severity != SeverityWarning {
		t.Errorf("invalid swap severity should fall back, got %s", cfg.Alerts.Swap.Severity)
	}
	if cfg.Alerts.Systemd.Severity != SeverityCritical {
		t.Errorf("invalid systemd severity should fall back, got %s", cfg.Alerts.Systemd.Severity)
	}
	if cfg.Alerts.HTTP.Severity != SeverityCritical {
		t.Errorf("invalid http severity should fall back, got %s", cfg.Alerts.HTTP.Severity)
	}
	if cfg.Alerts.HTTP.Checks[0].Timeout.Std() != 5*time.Second || cfg.Alerts.HTTP.Checks[0].ExpectedStatus != 200 {
		t.Errorf("http check defaults not applied: %+v", cfg.Alerts.HTTP.Checks[0])
	}
	if cfg.Alerts.TCP.Severity != SeverityCritical {
		t.Errorf("invalid tcp severity should fall back, got %s", cfg.Alerts.TCP.Severity)
	}
	if cfg.Alerts.TCP.Checks[0].Timeout.Std() != 5*time.Second {
		t.Errorf("tcp check defaults not applied: %+v", cfg.Alerts.TCP.Checks[0])
	}
	if cfg.Alerts.Process.Severity != SeverityCritical {
		t.Errorf("invalid process config should fall back, got %+v", cfg.Alerts.Process)
	}
	if cfg.Docker.Severity != SeverityCritical {
		t.Errorf("invalid docker severity should fall back, got %s", cfg.Docker.Severity)
	}
	if cfg.Docker.RestartThreshold != 3 || cfg.Docker.RestartWindow.Std() != 10*time.Minute {
		t.Errorf("invalid docker restart settings should fall back, got %+v", cfg.Docker)
	}
}

func TestLoadConfigResolveBelowDefaultsAndValidation(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
alerts:
  memory:
    threshold: 90
  disk:
    threshold: 3
    resolve_below: 4
  load:
    threshold: 8
  cpu:
    threshold: 70
  swap:
    threshold: 40
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Alerts.Memory.ResolveBelow == nil || *cfg.Alerts.Memory.ResolveBelow != 85 {
		t.Errorf("missing resolve_below should default to threshold-5, got %v", cfg.Alerts.Memory.ResolveBelow)
	}
	if cfg.Alerts.Disk.ResolveBelow == nil || *cfg.Alerts.Disk.ResolveBelow != 0 {
		t.Errorf("resolve_below above threshold should fall back to threshold-5 clamped at 0, got %v", cfg.Alerts.Disk.ResolveBelow)
	}
	if cfg.Alerts.Load.ResolveBelow == nil || *cfg.Alerts.Load.ResolveBelow != 7.5 {
		t.Errorf("missing load resolve_below should default to threshold-0.5, got %v", cfg.Alerts.Load.ResolveBelow)
	}
	if cfg.Alerts.CPU.ResolveBelow == nil || *cfg.Alerts.CPU.ResolveBelow != 65 {
		t.Errorf("missing cpu resolve_below should default to threshold-5, got %v", cfg.Alerts.CPU.ResolveBelow)
	}
	if cfg.Alerts.Swap.ResolveBelow == nil || *cfg.Alerts.Swap.ResolveBelow != 35 {
		t.Errorf("missing swap resolve_below should default to threshold-5, got %v", cfg.Alerts.Swap.ResolveBelow)
	}
}

// TestExampleConfigParses guards against drift between the shipped example
// config and the schema/defaults (e.g. a renamed key or a dropped field).
func TestExampleConfigParses(t *testing.T) {
	cfg, err := LoadConfig("deploy/config.example.yaml")
	if err != nil {
		t.Fatalf("example config should parse: %v", err)
	}
	if cfg.Alerts.Memory.Severity != SeverityWarning {
		t.Errorf("example memory severity: got %s", cfg.Alerts.Memory.Severity)
	}
	if len(cfg.Alerts.Disk.Mounts) != 1 || cfg.Alerts.Disk.Mounts[0] != "/" {
		t.Errorf("example disk mounts: got %v", cfg.Alerts.Disk.Mounts)
	}
}

func TestLoadConfigMissingExplicitPathErrors(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("expected an error for a missing explicit config path")
	}
}
