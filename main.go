package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/moby/moby/client"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cfg := LoadConfig()

	// shut down cleanly on SIGINT/SIGTERM (systemd stops the service with SIGTERM)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var notifiers []Notifier
	if cfg.GoogleChatWebhookURL != "" {
		notifiers = append(notifiers, &GoogleChatNotifier{WebhookURL: cfg.GoogleChatWebhookURL})
	}
	if cfg.DiscordWebhookURL != "" {
		notifiers = append(notifiers, &DiscordNotifier{WebhookURL: cfg.DiscordWebhookURL})
	}
	if cfg.SlackWebhookURL != "" {
		notifiers = append(notifiers, &SlackNotifier{WebhookURL: cfg.SlackWebhookURL})
	}
	if cfg.GenericWebhookURL != "" {
		notifiers = append(notifiers, &GenericWebhookNotifier{WebhookURL: cfg.GenericWebhookURL})
	}
	if cfg.TelegramBotToken != "" && cfg.TelegramChatID != "" {
		notifiers = append(notifiers, &TelegramNotifier{BotToken: cfg.TelegramBotToken, ChatID: cfg.TelegramChatID})
	}
	if cfg.SMTPHost != "" && cfg.SMTPFrom != "" && cfg.SMTPTo != "" {
		var to []string
		for _, addr := range strings.Split(cfg.SMTPTo, ",") {
			if a := strings.TrimSpace(addr); a != "" {
				to = append(to, a)
			}
		}
		notifiers = append(notifiers, &SMTPNotifier{
			Host: cfg.SMTPHost, Port: cfg.SMTPPort,
			Username: cfg.SMTPUsername, Password: cfg.SMTPPassword,
			From: cfg.SMTPFrom, To: to,
		})
	}
	if len(notifiers) == 0 {
		notifiers = append(notifiers, &ConsoleNotifier{})
		fmt.Println("No webhook configured; alerts will print to the console")
	}

	// Severity is hardcoded until the YAML config lands (issue #7).
	rules := []Rule{
		{
			ID:        "memory",
			Matcher:   func(s MetricSample) bool { return s.Name == "memory.used_percent" },
			Threshold: cfg.MemThreshold,
			Message:   "High memory usage",
			Severity:  SeverityCritical,
			For:       cfg.MemFor,
		},
		{
			ID:        "disk",
			Matcher:   func(s MetricSample) bool { return s.Collector == "disk" && strings.HasSuffix(s.Name, ".used_percent") },
			Threshold: cfg.DiskThreshold,
			Message:   "High disk usage",
			Severity:  SeverityWarning,
			For:       cfg.DiskFor,
		},
	}

	alertManager := NewAlertManager(rules, cfg.RenotifyAfter, cfg.Hostname, notifiers)

	// only build the docker client when enabled; client.New would otherwise fail startup
	var cli *client.Client
	if cfg.DockerEnabled {
		var err error
		cli, err = client.New(client.FromEnv)
		if err != nil {
			fmt.Printf("failed to create docker client: %v\n", err)
			os.Exit(1)
		}
		defer cli.Close()
	}

	if cfg.HeartbeatURL != "" {
		go func() {
			if err := PingRemote(cfg.HeartbeatURL); err != nil {
				fmt.Printf("Initial heartbeat failed: %v\n", err)
			}

			ticker := time.NewTicker(cfg.HeartbeatInterval)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := PingRemote(cfg.HeartbeatURL); err != nil {
						fmt.Printf("Heartbeat failed: %v\n", err)
					}
				}
			}
		}()
	}

	incomingSamplesChannel := make(chan MetricSample, 100)
	dockerEventsChannel := make(chan DockerEvent, 100)
	dockerEventsEvaluationChannel := make(chan string, 100)
	containers := make(map[string]*ContainerState)

	// single evaluator goroutine owns the alert + container state, so no mutex is needed
	go func() {
		for {
			select {
			case metric, ok := <-incomingSamplesChannel:
				if !ok {
					return
				}
				fmt.Printf("  metric: %s = %.2f %s\n", metric.Name, metric.Value, metric.Unit)
				alertManager.Evaluate(metric)

			case dockerEvent, ok := <-dockerEventsChannel:
				if !ok {
					return
				}
				handleDockerEvent(containers, dockerEvent, dockerEventsEvaluationChannel)

			case pendingContainerToEvaluate, ok := <-dockerEventsEvaluationChannel:
				if !ok {
					continue
				}
				if container := containers[pendingContainerToEvaluate]; container != nil {
					evaluateContainer(container)
				}
			}
		}
	}()

	fmt.Printf("Starting lookout %s on %s (Ctrl+C to stop)\n", version, cfg.Hostname)

	if cfg.DockerEnabled {
		go dockerCollector(cli, ctx, dockerEventsChannel)
	}

	collect := func() {
		data, err := memoryCollector(cfg.MeminfoPath)
		if err != nil {
			fmt.Printf("Error collecting memory info: %v\n", err)
		} else {
			for _, d := range data {
				incomingSamplesChannel <- d
			}
		}

		diskData, err := diskCollector(cfg.DiskInfoPath, cfg.TargetMounts)
		if err != nil {
			fmt.Printf("Error collecting disk info: %v\n", err)
		} else {
			for _, d := range diskData {
				incomingSamplesChannel <- d
			}
		}
	}

	ticker := time.NewTicker(cfg.CollectionInterval)
	defer ticker.Stop()

	collect() // collect once at startup instead of waiting a full interval
	for {
		select {
		case <-ctx.Done():
			fmt.Println("Shutting down")
			return
		case <-ticker.C:
			collect()
		}
	}
}
