package main

import (
	"context"
	"flag"
	"fmt"
	"log"
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
	configPath := flag.String("config", defaultConfigPath, "path to the config file")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// shut down cleanly on SIGINT/SIGTERM (systemd stops the service with SIGTERM)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	notifiers := buildNotifiers(cfg.Notifiers)
	if len(notifiers) == 0 {
		notifiers = append(notifiers, &ConsoleNotifier{})
		fmt.Println("No notifier configured; alerts will print to the console")
	}

	rules := []Rule{
		{
			ID:        "memory",
			Matcher:   func(s MetricSample) bool { return s.Name == "memory.used_percent" },
			Threshold: cfg.Alerts.Memory.Threshold,
			Message:   "High memory usage",
			Severity:  cfg.Alerts.Memory.Severity,
			For:       cfg.Alerts.Memory.For.Std(),
		},
		{
			ID:        "disk",
			Matcher:   func(s MetricSample) bool { return s.Collector == "disk" && strings.HasSuffix(s.Name, ".used_percent") },
			Threshold: cfg.Alerts.Disk.Threshold,
			Message:   "High disk usage",
			Severity:  cfg.Alerts.Disk.Severity,
			For:       cfg.Alerts.Disk.For.Std(),
		},
	}

	alertManager := NewAlertManager(rules, cfg.Alerts.RenotifyAfter.Std(), cfg.Hostname, notifiers)

	// only build the docker client when enabled; client.New would otherwise fail startup
	var cli *client.Client
	if cfg.Docker.Enabled {
		var err error
		cli, err = client.New(client.FromEnv)
		if err != nil {
			fmt.Printf("failed to create docker client: %v\n", err)
			os.Exit(1)
		}
		defer cli.Close()
	}

	if cfg.Heartbeat.URL != "" {
		go func() {
			if err := PingRemote(cfg.Heartbeat.URL); err != nil {
				fmt.Printf("Initial heartbeat failed: %v\n", err)
			}

			ticker := time.NewTicker(cfg.Heartbeat.Interval.Std())
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := PingRemote(cfg.Heartbeat.URL); err != nil {
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

	if cfg.Docker.Enabled {
		go dockerCollector(cli, ctx, dockerEventsChannel)
	}

	collect := func() {
		data, err := memoryCollector(cfg.Alerts.Memory.Source)
		if err != nil {
			fmt.Printf("Error collecting memory info: %v\n", err)
		} else {
			for _, d := range data {
				incomingSamplesChannel <- d
			}
		}

		diskData, err := diskCollector(cfg.Alerts.Disk.Source, cfg.Alerts.Disk.Mounts)
		if err != nil {
			fmt.Printf("Error collecting disk info: %v\n", err)
		} else {
			for _, d := range diskData {
				incomingSamplesChannel <- d
			}
		}
	}

	ticker := time.NewTicker(cfg.CollectionInterval.Std())
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

// buildNotifiers maps the configured notifier sections to live notifiers. A nil
// section (absent from the config) or one missing its credentials is skipped.
func buildNotifiers(cfg NotifiersConfig) []Notifier {
	var notifiers []Notifier
	if n := cfg.GoogleChat; n != nil && n.WebhookURL != "" {
		notifiers = append(notifiers, &GoogleChatNotifier{WebhookURL: n.WebhookURL})
	}
	if n := cfg.Discord; n != nil && n.WebhookURL != "" {
		notifiers = append(notifiers, &DiscordNotifier{WebhookURL: n.WebhookURL})
	}
	if n := cfg.Slack; n != nil && n.WebhookURL != "" {
		notifiers = append(notifiers, &SlackNotifier{WebhookURL: n.WebhookURL})
	}
	if n := cfg.Webhook; n != nil && n.URL != "" {
		notifiers = append(notifiers, &GenericWebhookNotifier{WebhookURL: n.URL})
	}
	if n := cfg.Telegram; n != nil && n.BotToken != "" && n.ChatID != "" {
		notifiers = append(notifiers, &TelegramNotifier{BotToken: n.BotToken, ChatID: n.ChatID})
	}
	if n := cfg.Email; n != nil && n.Host != "" && n.From != "" && len(n.To) > 0 {
		notifiers = append(notifiers, &SMTPNotifier{
			Host: n.Host, Port: n.Port,
			Username: n.Username, Password: n.Password,
			From: n.From, To: n.To,
		})
	}
	return notifiers
}
