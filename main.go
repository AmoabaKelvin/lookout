package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/moby/moby/client"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

const notifierQueueSize = 100

// Linux kernel files the collectors parse. These are not user-configurable:
// each collector parses a kernel-specific format, so the path can only be the
// real one. The collectors still take a path argument so tests can pass fixtures.
const (
	procMeminfo = "/proc/meminfo"
	procMounts  = "/proc/mounts"
	procLoadavg = "/proc/loadavg"
	procStat    = "/proc/stat"
	procDir     = "/proc"
)

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

	// A malformed heartbeat URL never pings, which silently inverts the
	// dead-man's switch into a permanent false "down" alert, so fail fast.
	if cfg.Heartbeat.URL != "" {
		if err := validateWebhookURL("heartbeat.url", cfg.Heartbeat.URL); err != nil {
			log.Fatalf("heartbeat: %v", err)
		}
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
			Matcher:      func(s MetricSample) bool { return s.Name == "load.1_per_core" },
			Threshold:    cfg.Alerts.Load.Threshold,
			ResolveBelow: *cfg.Alerts.Load.ResolveBelow,
			Message:      "High 1-minute load per core",
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
		{
			ID:           "swap",
			Matcher:      func(s MetricSample) bool { return s.Name == "swap.used_percent" },
			Threshold:    cfg.Alerts.Swap.Threshold,
			ResolveBelow: *cfg.Alerts.Swap.ResolveBelow,
			Message:      "High swap usage",
			Severity:     cfg.Alerts.Swap.Severity,
			For:          cfg.Alerts.Swap.For.Std(),
		},
	}
	if cfg.Alerts.Disk.PredictFullWithin.Std() > 0 {
		rules = append(rules, Rule{
			ID: "disk-fill",
			Matcher: func(s MetricSample) bool {
				return s.Collector == "disk" && strings.HasSuffix(s.Name, ".fills_within_window")
			},
			Threshold:    0,
			ResolveBelow: 0,
			Message:      "Disk predicted to fill soon",
			Severity:     cfg.Alerts.Disk.Severity,
			For:          cfg.Alerts.Disk.For.Std(),
		})
	}
	for _, family := range checkFamilies(cfg) {
		for _, dc := range family.derived() {
			metricName := dc.metricName
			rules = append(rules, Rule{
				ID:       dc.ruleID,
				Matcher:  func(s MetricSample) bool { return s.Name == metricName },
				Message:  family.message + dc.label,
				Severity: family.severity,
			})
		}
	}

	alertManager := NewAlertManager(rules, cfg.Alerts.RenotifyAfter.Std(), cfg.Hostname, []Notifier{notifierQueue})
	alertManager.stateFile = cfg.StateFile
	alertManager.staleAfter = cfg.Alerts.StaleAfter.Std()
	alertManager.tracked = trackedMetrics(cfg)
	if err := alertManager.LoadState(); err != nil {
		log.Fatalf("alert state: %v", err)
	}

	// only build the docker client when enabled; client.New would otherwise fail startup
	var cli *client.Client
	if cfg.Docker.Enabled {
		var err error
		cli, err = client.New(client.FromEnv)
		if err != nil {
			log.Fatalf("docker: %v", err)
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
		go dockerCollector(ctx, cli, dockerEventsChannel)
	}

	var cpuPrevious *cpuTimes
	diskPredictor := newDiskFillPredictor()
	publish := func(samples []MetricSample) {
		for _, d := range samples {
			evaluatorEvents <- evaluatorEvent{sample: d, hasSample: true}
		}
	}
	emit := func(label string, samples []MetricSample, err error) {
		if err != nil {
			fmt.Printf("Error collecting %s info: %v\n", label, err)
			return
		}
		publish(samples)
	}
	collect := func() {
		memData, err := memoryCollector(procMeminfo)
		emit("memory", memData, err)

		diskData, err := diskCollector(procMounts, cfg.Alerts.Disk.Mounts)
		if err != nil {
			fmt.Printf("Error collecting disk info: %v\n", err)
		} else {
			publish(diskData)
			publish(diskPredictor.collect(diskData, cfg.Alerts.Disk.PredictFullWithin.Std()))
		}

		loadData, err := loadCollector(procLoadavg, runtime.NumCPU())
		emit("load", loadData, err)

		// CPU is the one collector that threads state (cpuPrevious) between runs.
		cpuData, nextCPU, err := cpuCollector(procStat, cpuPrevious)
		if err != nil {
			fmt.Printf("Error collecting cpu info: %v\n", err)
		} else {
			cpuPrevious = nextCPU
			publish(cpuData)
		}

		publish(systemdCollector(cfg.Alerts.Systemd.Services))
		publish(httpCollector(cfg.Alerts.HTTP.Checks))
		publish(tcpCollector(cfg.Alerts.TCP.Checks))
		publish(processCollector(procDir, cfg.Alerts.Process.Names))

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
		{RuleID: "load", Name: "load.1_per_core"},
		{RuleID: "cpu", Name: "cpu.used_percent"},
		{RuleID: "swap", Name: "swap.used_percent"},
	}
	for _, mount := range cfg.Alerts.Disk.Mounts {
		name := mountPointToName(mount)
		tracked = append(tracked, TrackedMetric{
			RuleID: "disk",
			Name:   "disk." + name + ".used_percent",
		})
		if cfg.Alerts.Disk.PredictFullWithin.Std() > 0 {
			tracked = append(tracked, TrackedMetric{
				RuleID: "disk-fill",
				Name:   "disk." + name + ".fills_within_window",
			})
		}
	}
	for _, family := range checkFamilies(cfg) {
		for _, dc := range family.derived() {
			tracked = append(tracked, TrackedMetric{RuleID: dc.ruleID, Name: dc.metricName})
		}
	}
	return tracked
}

// derivedCheck is one name-derived check (a systemd unit, HTTP/TCP check, or
// process) after its display name has been resolved and sanitized.
type derivedCheck struct {
	ruleID     string
	metricName string
	label      string
}

// checkFamily groups name-derived checks that share a metric prefix, suffix,
// alert message, and severity. items holds {name, fallback} pairs: fallback is
// used (untrimmed, to match the collector) when name is blank.
type checkFamily struct {
	prefix   string
	suffix   string
	message  string
	severity Severity
	items    []checkItem
}

type checkItem struct{ name, fallback string }

// derived resolves each item to its rule ID and metric name. The resolution
// mirrors the collectors exactly so a rule's metric name matches what its
// collector emits.
func (f checkFamily) derived() []derivedCheck {
	var out []derivedCheck
	for _, it := range f.items {
		name := strings.TrimSpace(it.name)
		if name == "" {
			name = it.fallback
		}
		if strings.TrimSpace(name) == "" {
			continue
		}
		part := safeMetricPart(name)
		out = append(out, derivedCheck{
			ruleID:     f.prefix + "-" + part,
			metricName: f.prefix + "." + part + f.suffix,
			label:      name,
		})
	}
	return out
}

// checkFamilies returns the name-derived check families configured for cfg.
func checkFamilies(cfg Config) []checkFamily {
	systemd := make([]checkItem, len(cfg.Alerts.Systemd.Services))
	for i, s := range cfg.Alerts.Systemd.Services {
		systemd[i] = checkItem{name: s}
	}
	process := make([]checkItem, len(cfg.Alerts.Process.Names))
	for i, p := range cfg.Alerts.Process.Names {
		process[i] = checkItem{name: p}
	}
	http := make([]checkItem, len(cfg.Alerts.HTTP.Checks))
	for i, c := range cfg.Alerts.HTTP.Checks {
		http[i] = checkItem{name: c.Name, fallback: c.URL}
	}
	tcp := make([]checkItem, len(cfg.Alerts.TCP.Checks))
	for i, c := range cfg.Alerts.TCP.Checks {
		tcp[i] = checkItem{name: c.Name, fallback: c.Address}
	}
	return []checkFamily{
		{prefix: "systemd", suffix: ".unhealthy", message: "Systemd service unhealthy: ", severity: cfg.Alerts.Systemd.Severity, items: systemd},
		{prefix: "http", suffix: ".unhealthy", message: "HTTP check unhealthy: ", severity: cfg.Alerts.HTTP.Severity, items: http},
		{prefix: "tcp", suffix: ".unhealthy", message: "TCP check unhealthy: ", severity: cfg.Alerts.TCP.Severity, items: tcp},
		{prefix: "process", suffix: ".missing", message: "Process missing: ", severity: cfg.Alerts.Process.Severity, items: process},
	}
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
		if err := validateWebhookURL("notifiers.webhook.webhook_url", n.WebhookURL); err != nil {
			return nil, nil, err
		}
		notifiers = append(notifiers, &GenericWebhookNotifier{WebhookURL: n.WebhookURL})
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
