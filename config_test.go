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
	if cfg.Alerts.Memory.Threshold != 80 {
		t.Errorf("memory threshold: got %v", cfg.Alerts.Memory.Threshold)
	}
	if cfg.Alerts.Disk.For.Std() != 2*time.Minute {
		t.Errorf("disk for: got %s", cfg.Alerts.Disk.For.Std())
	}
	if len(cfg.Alerts.Disk.Mounts) == 0 {
		t.Errorf("expected default mounts")
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
collection_interval: 1m
alerts:
  memory:
    threshold: 90
    for: 30s
    severity: warning
  disk:
    mounts:
      - /
      - /data
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
	if cfg.Alerts.Memory.Threshold != 90 || cfg.Alerts.Memory.For.Std() != 30*time.Second {
		t.Errorf("memory overrides not applied: %+v", cfg.Alerts.Memory)
	}
	if cfg.Alerts.Memory.Severity != SeverityWarning {
		t.Errorf("severity override: got %s", cfg.Alerts.Memory.Severity)
	}
	if got := cfg.Alerts.Disk.Mounts; len(got) != 2 || got[1] != "/data" {
		t.Errorf("mounts override: got %v", got)
	}
	// disk threshold was not in the file, so its default must survive
	if cfg.Alerts.Disk.Threshold != 85 {
		t.Errorf("disk threshold default lost: got %v", cfg.Alerts.Disk.Threshold)
	}
	if cfg.Notifiers.Slack == nil || cfg.Notifiers.Slack.WebhookURL == "" {
		t.Errorf("slack notifier not parsed")
	}
	if cfg.Notifiers.Discord != nil {
		t.Errorf("absent notifier should be nil, got %+v", cfg.Notifiers.Discord)
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
}

func TestLoadConfigMissingExplicitPathErrors(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("expected an error for a missing explicit config path")
	}
}
