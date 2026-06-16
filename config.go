package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

const defaultConfigPath = "/etc/lookout/config.yaml"
const defaultStateFile = "/var/lib/lookout/state.json"

// Duration wraps time.Duration so it can be read from YAML as a string like
// "30s", "2m" or "1h" instead of a raw nanosecond integer.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

type Config struct {
	CollectionInterval Duration        `yaml:"collection_interval"`
	StateFile          string          `yaml:"state_file"`
	Alerts             AlertsConfig    `yaml:"alerts"`
	Notifiers          NotifiersConfig `yaml:"notifiers"`
	Heartbeat          HeartbeatConfig `yaml:"heartbeat"`
	Docker             DockerConfig    `yaml:"docker"`

	Hostname string `yaml:"-"` // derived at load time, not from the file
}

type AlertsConfig struct {
	RenotifyAfter Duration        `yaml:"renotify_after"`
	StaleAfter    Duration        `yaml:"stale_after"`
	Memory        MemoryConfig    `yaml:"memory"`
	Disk          DiskConfig      `yaml:"disk"`
	Load          LoadAlertConfig `yaml:"load"`
}

type MemoryConfig struct {
	Threshold    float64  `yaml:"threshold"`
	ResolveBelow *float64 `yaml:"resolve_below"`
	For          Duration `yaml:"for"`
	Severity     Severity `yaml:"severity"`
	Source       string   `yaml:"source"`
}

type DiskConfig struct {
	Threshold    float64  `yaml:"threshold"`
	ResolveBelow *float64 `yaml:"resolve_below"`
	For          Duration `yaml:"for"`
	Severity     Severity `yaml:"severity"`
	Source       string   `yaml:"source"`
	Mounts       []string `yaml:"mounts"`
}

type LoadAlertConfig struct {
	Threshold    float64  `yaml:"threshold"`
	ResolveBelow *float64 `yaml:"resolve_below"`
	For          Duration `yaml:"for"`
	Severity     Severity `yaml:"severity"`
	Source       string   `yaml:"source"`
}

// Notifier sections are pointers so an absent section is nil (not configured)
// and a present one is enabled.
type NotifiersConfig struct {
	GoogleChat *WebhookConfig  `yaml:"google_chat"`
	Discord    *WebhookConfig  `yaml:"discord"`
	Slack      *WebhookConfig  `yaml:"slack"`
	Telegram   *TelegramConfig `yaml:"telegram"`
	Webhook    *GenericConfig  `yaml:"webhook"`
	Email      *EmailConfig    `yaml:"email"`
}

type WebhookConfig struct {
	WebhookURL string `yaml:"webhook_url"`
}

type TelegramConfig struct {
	BotToken string `yaml:"bot_token"`
	ChatID   string `yaml:"chat_id"`
}

type GenericConfig struct {
	URL string `yaml:"url"`
}

type EmailConfig struct {
	Host     string   `yaml:"host"`
	Port     int      `yaml:"port"`
	Username string   `yaml:"username"`
	Password string   `yaml:"password"`
	From     string   `yaml:"from"`
	To       []string `yaml:"to"`
}

type HeartbeatConfig struct {
	URL      string   `yaml:"url"`
	Interval Duration `yaml:"interval"`
}

type DockerConfig struct {
	Enabled bool `yaml:"enabled"`
}

func defaultConfig() Config {
	return Config{
		CollectionInterval: Duration(30 * time.Second),
		StateFile:          defaultStateFile,
		Alerts: AlertsConfig{
			RenotifyAfter: Duration(time.Hour),
			Memory: MemoryConfig{
				Threshold: 80,
				For:       Duration(2 * time.Minute),
				Severity:  SeverityCritical,
				Source:    "/proc/meminfo",
			},
			Disk: DiskConfig{
				Threshold: 85,
				For:       Duration(2 * time.Minute),
				Severity:  SeverityWarning,
				Source:    "/proc/mounts",
				Mounts:    []string{"/", "/home", "/var", "/boot"},
			},
			Load: LoadAlertConfig{
				Threshold: 4,
				For:       Duration(2 * time.Minute),
				Severity:  SeverityWarning,
				Source:    "/proc/loadavg",
			},
		},
		Heartbeat: HeartbeatConfig{Interval: Duration(60 * time.Second)},
		Docker:    DockerConfig{Enabled: false},
	}
}

// LoadConfig reads the YAML file at path over a base of defaults. A missing file
// at the default path is fine (run on defaults); a missing explicit path is an
// error. The config is validated (and clamped where sensible) before returning.
func LoadConfig(path string) (Config, error) {
	cfg := defaultConfig()

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parsing %s: %w", path, err)
		}
	case os.IsNotExist(err) && path == defaultConfigPath:
		log.Printf("config: %s not found; using defaults", path)
	default:
		return Config{}, fmt.Errorf("reading %s: %w", path, err)
	}

	cfg.Hostname, _ = os.Hostname()
	cfg.validate()
	return cfg, nil
}

// validate clamps out-of-range values back to safe ones with a warning, so a
// misconfigured file degrades gracefully instead of crashing or going silent.
func (c *Config) validate() {
	clampInterval(&c.CollectionInterval, 30*time.Second, "collection_interval")
	defaultString(&c.StateFile, defaultStateFile)
	clampInterval(&c.Heartbeat.Interval, 60*time.Second, "heartbeat.interval")
	clampInterval(&c.Alerts.RenotifyAfter, time.Hour, "alerts.renotify_after")
	defaultStaleAfter(&c.Alerts.StaleAfter, 3*c.CollectionInterval.Std())

	clampThreshold(&c.Alerts.Memory.Threshold, "alerts.memory.threshold")
	clampThreshold(&c.Alerts.Disk.Threshold, "alerts.disk.threshold")
	clampPositiveFloat(&c.Alerts.Load.Threshold, 4, "alerts.load.threshold")
	c.Alerts.Memory.ResolveBelow = normalizedResolveBelow(c.Alerts.Memory.ResolveBelow, c.Alerts.Memory.Threshold, 5, "alerts.memory.resolve_below")
	c.Alerts.Disk.ResolveBelow = normalizedResolveBelow(c.Alerts.Disk.ResolveBelow, c.Alerts.Disk.Threshold, 5, "alerts.disk.resolve_below")
	c.Alerts.Load.ResolveBelow = normalizedResolveBelow(c.Alerts.Load.ResolveBelow, c.Alerts.Load.Threshold, 1, "alerts.load.resolve_below")

	clampFor(&c.Alerts.Memory.For, "alerts.memory.for")
	clampFor(&c.Alerts.Disk.For, "alerts.disk.for")
	clampFor(&c.Alerts.Load.For, "alerts.load.for")

	clampSeverity(&c.Alerts.Memory.Severity, SeverityCritical, "alerts.memory.severity")
	clampSeverity(&c.Alerts.Disk.Severity, SeverityWarning, "alerts.disk.severity")
	clampSeverity(&c.Alerts.Load.Severity, SeverityWarning, "alerts.load.severity")
}

func defaultString(s *string, fallback string) {
	if *s == "" {
		*s = fallback
	}
}

func clampInterval(d *Duration, fallback time.Duration, name string) {
	if d.Std() <= 0 {
		log.Printf("config: %s must be positive; using %s", name, fallback)
		*d = Duration(fallback)
	}
}

func defaultStaleAfter(d *Duration, fallback time.Duration) {
	if d.Std() <= 0 {
		*d = Duration(fallback)
	}
}

func defaultResolveBelow(threshold float64, margin float64) float64 {
	resolveBelow := threshold - margin
	if resolveBelow < 0 {
		return 0
	}
	return resolveBelow
}

func clampThreshold(v *float64, name string) {
	if *v < 0 || *v > 100 {
		clamped := *v
		if clamped < 0 {
			clamped = 0
		} else {
			clamped = 100
		}
		log.Printf("config: %s must be between 0 and 100; using %.0f", name, clamped)
		*v = clamped
	}
}

func clampPositiveFloat(v *float64, fallback float64, name string) {
	if *v <= 0 {
		log.Printf("config: %s must be positive; using %.0f", name, fallback)
		*v = fallback
	}
}

func normalizedResolveBelow(v *float64, threshold float64, margin float64, name string) *float64 {
	fallback := defaultResolveBelow(threshold, margin)
	if v == nil {
		return &fallback
	}
	if *v < 0 || *v > threshold {
		log.Printf("config: %s must be between 0 and threshold %.0f; using %.0f", name, threshold, fallback)
		return &fallback
	}
	return v
}

func clampFor(d *Duration, name string) {
	if d.Std() < 0 {
		log.Printf("config: %s cannot be negative; using 0 (immediate)", name)
		*d = 0
	}
}

func clampSeverity(s *Severity, fallback Severity, name string) {
	if *s != SeverityWarning && *s != SeverityCritical {
		log.Printf("config: %s must be warning or critical; using %s", name, fallback)
		*s = fallback
	}
}
