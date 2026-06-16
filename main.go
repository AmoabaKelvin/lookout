package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
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

	notifiers, activeNotifiers, err := buildNotifiers(cfg.Notifiers)
	if err != nil {
		log.Fatalf("notifiers: %v", err)
	}
	if len(notifiers) == 0 {
		notifiers = append(notifiers, &ConsoleNotifier{})
		fmt.Println("No notifier configured; alerts will print to the console")
	} else {
		fmt.Printf("Active notifiers: %s\n", strings.Join(activeNotifiers, ", "))
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
		{
			ID:           "cpu",
			Matcher:      func(s MetricSample) bool { return s.Name == "cpu.used_percent" },
			Threshold:    cfg.Alerts.CPU.Threshold,
			ResolveBelow: *cfg.Alerts.CPU.ResolveBelow,
			Message:      "High CPU usage",
			Severity:     cfg.Alerts.CPU.Severity,
			For:          cfg.Alerts.CPU.For.Std(),
		},
	}
	for _, service := range cfg.Alerts.Systemd.Services {
		service := service
		rules = append(rules, Rule{
			ID:           "systemd-" + safeMetricPart(service),
			Matcher:      func(s MetricSample) bool { return s.Name == "systemd."+safeMetricPart(service)+".unhealthy" },
			Threshold:    0,
			ResolveBelow: 0,
			Message:      "Systemd service unhealthy: " + service,
			Severity:     cfg.Alerts.Systemd.Severity,
		})
	}
	for _, check := range cfg.Alerts.HTTP.Checks {
		checkName := check.Name
		if checkName == "" {
			checkName = check.URL
		}
		rules = append(rules, Rule{
			ID:           "http-" + safeMetricPart(checkName),
			Matcher:      func(s MetricSample) bool { return s.Name == "http."+safeMetricPart(checkName)+".unhealthy" },
			Threshold:    0,
			ResolveBelow: 0,
			Message:      "HTTP check unhealthy: " + checkName,
			Severity:     cfg.Alerts.HTTP.Severity,
		})
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
	dockerEventsEvaluationChannel := make(chan dockerEvaluation, 100)
	containers := make(map[string]*ContainerState)
	dockerStateFile := dockerStatePath(cfg.StateFile)
	if cfg.Docker.Enabled {
		containers, err = loadDockerState(dockerStateFile)
		if err != nil {
			log.Fatalf("docker state: %v", err)
		}
		snapshots, err := dockerContainerSnapshots(ctx, cli, containers)
		if err != nil {
			log.Printf("docker: failed to reconcile running containers: %v", err)
		} else {
			for _, alert := range reconcileDockerAlerts(containers, snapshots, cfg.Hostname, cfg.Docker) {
				alertManager.dispatch(alert)
			}
			if err := saveDockerState(dockerStateFile, containers); err != nil {
				log.Printf("error saving docker state: %v", err)
			}
		}
	}
	saveDockerStateFile := func() {
		if !cfg.Docker.Enabled {
			return
		}
		if err := saveDockerState(dockerStateFile, containers); err != nil {
			log.Printf("error saving docker state: %v", err)
		}
	}

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
				if alerts := handleDockerEvent(containers, dockerEvent, dockerEventsEvaluationChannel, cfg.Hostname, cfg.Docker); len(alerts) > 0 {
					for _, alert := range alerts {
						alertManager.dispatch(alert)
					}
					saveDockerStateFile()
				}

			case pendingEvaluation, ok := <-dockerEventsEvaluationChannel:
				if !ok {
					continue
				}
				if container := containers[pendingEvaluation.ID]; container != nil {
					var alert *Alert
					switch pendingEvaluation.Kind {
					case "restart":
						alert = evaluateDockerRestartLoop(container, cfg.Hostname, cfg.Docker, time.Now())
					default:
						alert = evaluateContainer(container, cfg.Hostname, cfg.Docker)
					}
					if alert != nil {
						alertManager.dispatch(*alert)
						saveDockerStateFile()
					}
				}
			}
		}
	}()

	fmt.Printf("Starting lookout %s on %s (Ctrl+C to stop)\n", version, cfg.Hostname)

	if cfg.Docker.Enabled {
		go dockerCollector(cli, ctx, dockerEventsChannel)
	}

	var cpuPrevious *cpuTimes
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

		cpuData, nextCPU, err := cpuCollector(cfg.Alerts.CPU.Source, cpuPrevious)
		if err != nil {
			fmt.Printf("Error collecting cpu info: %v\n", err)
		} else {
			cpuPrevious = nextCPU
			for _, d := range cpuData {
				evaluatorEvents <- evaluatorEvent{sample: d, hasSample: true}
			}
		}

		for _, d := range systemdCollector(cfg.Alerts.Systemd.Services) {
			evaluatorEvents <- evaluatorEvent{sample: d, hasSample: true}
		}

		for _, d := range httpCollector(cfg.Alerts.HTTP.Checks) {
			evaluatorEvents <- evaluatorEvent{sample: d, hasSample: true}
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
		{RuleID: "cpu", Name: "cpu.used_percent"},
	}
	for _, mount := range cfg.Alerts.Disk.Mounts {
		tracked = append(tracked, TrackedMetric{
			RuleID: "disk",
			Name:   "disk." + mountPointToName(mount) + ".used_percent",
		})
	}
	for _, service := range cfg.Alerts.Systemd.Services {
		tracked = append(tracked, TrackedMetric{
			RuleID: "systemd-" + safeMetricPart(service),
			Name:   "systemd." + safeMetricPart(service) + ".unhealthy",
		})
	}
	for _, check := range cfg.Alerts.HTTP.Checks {
		name := check.Name
		if name == "" {
			name = check.URL
		}
		tracked = append(tracked, TrackedMetric{
			RuleID: "http-" + safeMetricPart(name),
			Name:   "http." + safeMetricPart(name) + ".unhealthy",
		})
	}
	return tracked
}

// buildNotifiers maps configured notifier sections to live notifiers. A nil
// section is absent; a present but incomplete section is a startup error.
func buildNotifiers(cfg NotifiersConfig) ([]Notifier, []string, error) {
	var notifiers []Notifier
	var active []string

	if n := cfg.GoogleChat; n != nil {
		if err := validateWebhookURL("notifiers.google_chat.webhook_url", n.WebhookURL); err != nil {
			return nil, nil, err
		}
		notifiers = append(notifiers, &GoogleChatNotifier{WebhookURL: n.WebhookURL})
		active = append(active, "google_chat")
	}
	if n := cfg.Discord; n != nil {
		if err := validateWebhookURL("notifiers.discord.webhook_url", n.WebhookURL); err != nil {
			return nil, nil, err
		}
		notifiers = append(notifiers, &DiscordNotifier{WebhookURL: n.WebhookURL})
		active = append(active, "discord")
	}
	if n := cfg.Slack; n != nil {
		if err := validateWebhookURL("notifiers.slack.webhook_url", n.WebhookURL); err != nil {
			return nil, nil, err
		}
		notifiers = append(notifiers, &SlackNotifier{WebhookURL: n.WebhookURL})
		active = append(active, "slack")
	}
	if n := cfg.Teams; n != nil {
		if err := validateWebhookURL("notifiers.teams.webhook_url", n.WebhookURL); err != nil {
			return nil, nil, err
		}
		notifiers = append(notifiers, &TeamsNotifier{WebhookURL: n.WebhookURL})
		active = append(active, "teams")
	}
	if n := cfg.Webhook; n != nil {
		if err := validateWebhookURL("notifiers.webhook.url", n.URL); err != nil {
			return nil, nil, err
		}
		notifiers = append(notifiers, &GenericWebhookNotifier{WebhookURL: n.URL})
		active = append(active, "webhook")
	}
	if n := cfg.Telegram; n != nil {
		if n.BotToken == "" {
			return nil, nil, fmt.Errorf("notifiers.telegram.bot_token is required")
		}
		if n.ChatID == "" {
			return nil, nil, fmt.Errorf("notifiers.telegram.chat_id is required")
		}
		notifiers = append(notifiers, &TelegramNotifier{BotToken: n.BotToken, ChatID: n.ChatID})
		active = append(active, "telegram")
	}
	if n := cfg.PagerDuty; n != nil {
		if n.IntegrationKey == "" {
			return nil, nil, fmt.Errorf("notifiers.pagerduty.integration_key is required")
		}
		notifiers = append(notifiers, &PagerDutyNotifier{IntegrationKey: n.IntegrationKey})
		active = append(active, "pagerduty")
	}
	if n := cfg.Email; n != nil {
		if err := validateEmailNotifier(n); err != nil {
			return nil, nil, err
		}
		notifiers = append(notifiers, &SMTPNotifier{
			Host: n.Host, Port: n.Port, ImplicitTLS: n.ImplicitTLS,
			Username: n.Username, Password: n.Password,
			From: n.From, To: n.To,
		})
		active = append(active, "email")
	}
	return notifiers, active, nil
}

func validateWebhookURL(name string, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%s must be a valid http or https URL", name)
	}
	return nil
}

func validateEmailNotifier(n *EmailConfig) error {
	if n.Host == "" {
		return fmt.Errorf("notifiers.email.host is required")
	}
	if n.Port <= 0 || n.Port > 65535 {
		return fmt.Errorf("notifiers.email.port must be between 1 and 65535")
	}
	if n.From == "" {
		return fmt.Errorf("notifiers.email.from is required")
	}
	if len(n.To) == 0 {
		return fmt.Errorf("notifiers.email.to must contain at least one recipient")
	}
	return nil
}
