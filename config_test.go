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
	if cfg.Alerts.Memory.Threshold != 80 {
		t.Errorf("memory threshold: got %v", cfg.Alerts.Memory.Threshold)
	}
	if cfg.Alerts.StaleAfter.Std() != 90*time.Second {
		t.Errorf("stale after: got %s", cfg.Alerts.StaleAfter.Std())
	}
	if cfg.Alerts.Memory.ResolveBelow == nil || *cfg.Alerts.Memory.ResolveBelow != 75 {
		t.Errorf("memory resolve below: got %v", cfg.Alerts.Memory.ResolveBelow)
	}
	if cfg.Alerts.Disk.ResolveBelow == nil || *cfg.Alerts.Disk.ResolveBelow != 80 {
		t.Errorf("disk resolve below: got %v", cfg.Alerts.Disk.ResolveBelow)
	}
	if cfg.Alerts.Disk.For.Std() != 2*time.Minute {
		t.Errorf("disk for: got %s", cfg.Alerts.Disk.For.Std())
	}
	if cfg.Alerts.Load.Threshold != 4 {
		t.Errorf("load threshold: got %v", cfg.Alerts.Load.Threshold)
	}
	if cfg.Alerts.Load.ResolveBelow == nil || *cfg.Alerts.Load.ResolveBelow != 3 {
		t.Errorf("load resolve below: got %v", cfg.Alerts.Load.ResolveBelow)
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
    mounts:
      - /
      - /data
  load:
    threshold: 8
    resolve_below: 6
    for: 1m
    severity: critical
notifiers:
  slack:
    webhook_url: "https://hooks.slack.com/services/X/Y/Z"
heartbeat:
  url: "https://hc-ping.com/abc"
  interval: 45s
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
	if cfg.Alerts.Load.Threshold != 8 || cfg.Alerts.Load.For.Std() != time.Minute {
		t.Errorf("load overrides not applied: %+v", cfg.Alerts.Load)
	}
	if cfg.Alerts.Load.ResolveBelow == nil || *cfg.Alerts.Load.ResolveBelow != 6 {
		t.Errorf("load resolve below override: got %v", cfg.Alerts.Load.ResolveBelow)
	}
	if cfg.Alerts.Load.Severity != SeverityCritical {
		t.Errorf("load severity override: got %s", cfg.Alerts.Load.Severity)
	}
	if cfg.Notifiers.Slack == nil || cfg.Notifiers.Slack.WebhookURL == "" {
		t.Errorf("slack notifier not parsed")
	}
	if cfg.Notifiers.Discord != nil {
		t.Errorf("absent notifier should be nil, got %+v", cfg.Notifiers.Discord)
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
	if cfg.Alerts.Load.Threshold != 4 {
		t.Errorf("non-positive load threshold should fall back, got %v", cfg.Alerts.Load.Threshold)
	}
	if cfg.Alerts.Load.Severity != SeverityWarning {
		t.Errorf("invalid load severity should fall back, got %s", cfg.Alerts.Load.Severity)
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
	if cfg.Alerts.Load.ResolveBelow == nil || *cfg.Alerts.Load.ResolveBelow != 7 {
		t.Errorf("missing load resolve_below should default to threshold-1, got %v", cfg.Alerts.Load.ResolveBelow)
	}
}

func TestLoadConfigMissingExplicitPathErrors(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("expected an error for a missing explicit config path")
	}
}
