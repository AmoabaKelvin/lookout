package main

import (
	"os"
	"strconv"
	"time"
)

// Config is loaded from environment variables (YAML config planned, issue #7).
type Config struct {
	CollectionInterval   time.Duration
	MeminfoPath          string
	MemThreshold         float64
	DiskInfoPath         string
	DiskThreshold        float64
	TargetMounts         []string
	HeartbeatURL         string
	HeartbeatInterval    time.Duration
	GoogleChatWebhookURL string
	DiscordWebhookURL    string
	SlackWebhookURL      string
	GenericWebhookURL    string
	TelegramBotToken     string
	TelegramChatID       string
	SMTPHost             string
	SMTPPort             string
	SMTPUsername         string
	SMTPPassword         string
	SMTPFrom             string
	SMTPTo               string // comma-separated recipients
	Hostname             string
	RenotifyAfter        time.Duration
	DockerEnabled        bool
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func getEnvAsBool(key string, fallback bool) bool {
	if value, exists := os.LookupEnv(key); exists {
		return value == "true"
	}
	return fallback
}

func getEnvAsInt(key string, fallback int) int {
	if value, exists := os.LookupEnv(key); exists {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func getEnvAsFloat64(key string, fallback float64) float64 {
	if value, exists := os.LookupEnv(key); exists {
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			return parsed
		}
	}
	return fallback
}

func LoadConfig() Config {
	hostName, _ := os.Hostname()

	cfg := Config{
		CollectionInterval:   time.Duration(getEnvAsInt("COLLECTION_INTERVAL", 30)) * time.Second,
		MeminfoPath:          getEnv("MEMINFO_PATH", "/proc/meminfo"),
		MemThreshold:         getEnvAsFloat64("MEM_THRESHOLD", 80),
		DiskInfoPath:         getEnv("DISKINFO_PATH", "/proc/mounts"),
		DiskThreshold:        getEnvAsFloat64("DISK_THRESHOLD", 85),
		TargetMounts:         []string{"/", "/home", "/var", "/boot"},
		HeartbeatURL:         getEnv("HEARTBEAT_URL", ""),
		HeartbeatInterval:    time.Duration(getEnvAsInt("HEARTBEAT_INTERVAL", 60)) * time.Second,
		GoogleChatWebhookURL: getEnv("GOOGLE_CHAT_WEBHOOK_URL", ""),
		DiscordWebhookURL:    getEnv("DISCORD_WEBHOOK_URL", ""),
		SlackWebhookURL:      getEnv("SLACK_WEBHOOK_URL", ""),
		GenericWebhookURL:    getEnv("WEBHOOK_URL", ""),
		TelegramBotToken:     getEnv("TELEGRAM_BOT_TOKEN", ""),
		TelegramChatID:       getEnv("TELEGRAM_CHAT_ID", ""),
		SMTPHost:             getEnv("SMTP_HOST", ""),
		SMTPPort:             getEnv("SMTP_PORT", "587"),
		SMTPUsername:         getEnv("SMTP_USERNAME", ""),
		SMTPPassword:         getEnv("SMTP_PASSWORD", ""),
		SMTPFrom:             getEnv("SMTP_FROM", ""),
		SMTPTo:               getEnv("SMTP_TO", ""),
		Hostname:             hostName,
		RenotifyAfter:        time.Duration(getEnvAsInt("RENOTIFY_AFTER", 3600)) * time.Second,
		DockerEnabled:        getEnvAsBool("DOCKER_ENABLED", false),
	}

	return cfg
}
