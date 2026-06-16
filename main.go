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

const notifierQueueSize = 100

type evaluatorEvent struct {
	sample     MetricSample
	hasSample  bool
	checkStale bool
}

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
	notifierQueue := newAsyncNotifier(notifiers, notifierQueueSize)
	go notifierQueue.Run(ctx)

	rules := []Rule{
		{
			ID:           "memory",
			Matcher:      func(s MetricSample) bool { return s.Name == "memory.used_percent" },
			Threshold:    cfg.Alerts.Memory.Threshold,
			ResolveBelow: *cfg.Alerts.Memory.ResolveBelow,
			Message:      "High memory usage",
			Severity:     cfg.Alerts.Memory.Severity,
			For:          cfg.Alerts.Memory.For.Std(),
		},
		{
			ID:           "disk",
			Matcher:      func(s MetricSample) bool { return s.Collector == "disk" && strings.HasSuffix(s.Name, ".used_percent") },
			Threshold:    cfg.Alerts.Disk.Threshold,
			ResolveBelow: *cfg.Alerts.Disk.ResolveBelow,
			Message:      "High disk usage",
			Severity:     cfg.Alerts.Disk.Severity,
			For:          cfg.Alerts.Disk.For.Std(),
		},
		{
			ID:           "load",
			Matcher:      func(s MetricSample) bool { return s.Name == "load.1" },
			Threshold:    cfg.Alerts.Load.Threshold,
			ResolveBelow: *cfg.Alerts.Load.ResolveBelow,
			Message:      "High 1-minute load average",
			Severity:     cfg.Alerts.Load.Severity,
			For:          cfg.Alerts.Load.For.Std(),
		},
	}

	alertManager := NewAlertManager(rules, cfg.Alerts.RenotifyAfter.Std(), cfg.Hostname, []Notifier{notifierQueue})
	alertManager.StateFile = cfg.StateFile
	alertManager.StaleAfter = cfg.Alerts.StaleAfter.Std()
	alertManager.Tracked = trackedMetrics(cfg)
	if err := alertManager.LoadState(cfg.StateFile); err != nil {
		log.Fatalf("alert state: %v", err)
	}

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

	evaluatorEvents := make(chan evaluatorEvent, 100)
	dockerEventsChannel := make(chan DockerEvent, 100)
	dockerEventsEvaluationChannel := make(chan string, 100)
	containers := make(map[string]*ContainerState)

	// single evaluator goroutine owns the alert + container state, so no mutex is needed
	go func() {
		for {
			select {
			case event, ok := <-evaluatorEvents:
				if !ok {
					return
				}
				if event.hasSample {
					alertManager.Evaluate(event.sample)
				}
				if event.checkStale {
					alertManager.CheckStale()
				}

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
				evaluatorEvents <- evaluatorEvent{sample: d, hasSample: true}
			}
		}

		diskData, err := diskCollector(cfg.Alerts.Disk.Source, cfg.Alerts.Disk.Mounts)
		if err != nil {
			fmt.Printf("Error collecting disk info: %v\n", err)
		} else {
			for _, d := range diskData {
				evaluatorEvents <- evaluatorEvent{sample: d, hasSample: true}
			}
		}

		loadData, err := loadCollector(cfg.Alerts.Load.Source)
		if err != nil {
			fmt.Printf("Error collecting load info: %v\n", err)
		} else {
			for _, d := range loadData {
				evaluatorEvents <- evaluatorEvent{sample: d, hasSample: true}
			}
		}

		evaluatorEvents <- evaluatorEvent{checkStale: true}
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

func trackedMetrics(cfg Config) []TrackedMetric {
	tracked := []TrackedMetric{
		{RuleID: "memory", Name: "memory.used_percent"},
		{RuleID: "load", Name: "load.1"},
	}
	for _, mount := range cfg.Alerts.Disk.Mounts {
		tracked = append(tracked, TrackedMetric{
			RuleID: "disk",
			Name:   "disk." + mountPointToName(mount) + ".used_percent",
		})
	}
	return tracked
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
