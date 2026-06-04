package main

import (
	"os"
	"strconv"
	"time"
)

// for now we are parsing the configuration as env
// variables, the idea though was to use a config file
// that will still be done but i just wanted a quick way
// to get somethign up and running and this looked like
// the fastest option without dealing with parsing
// files and stuff
type Config struct {
	CollectionInterval   time.Duration // the interval at which to collect metrics
	MeminfoPath          string
	MemThreshold         float64
	DiskInfoPath         string
	DiskThreshold        float64
	TargetMounts         []string
	HeartbeatURL         string
	HeartbeatInterval    time.Duration
	GoogleChatWebhookURL string
	DiscordWebhookURL    string
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
		Hostname:             hostName,
		RenotifyAfter:        time.Duration(getEnvAsInt("RENOTIFY_AFTER", 3600)) * time.Second,
		DockerEnabled:        getEnvAsBool("DOCKER_ENABLED", false),
	}

	return cfg
}
