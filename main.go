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

	notifiers, err := initNotifiers(cfg.Notifiers)
	if err != nil {
		log.Fatalf("notifiers: %v", err)
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

	metricSnapshot := newMetricsSnapshot(cfg.Hostname, version)
	if err := startMetricsServer(ctx, cfg.Metrics, metricSnapshot); err != nil {
		log.Fatalf("metrics: %v", err)
	}

	alertManager := NewAlertManager(buildRules(cfg), cfg.Alerts.RenotifyAfter.Std(), cfg.Hostname, []Notifier{notifierQueue})
	alertManager.stateFile = cfg.StateFile
	alertManager.staleAfter = cfg.Alerts.StaleAfter.Std()
	alertManager.tracked = trackedMetrics(cfg)
	if err := alertManager.LoadState(); err != nil {
		log.Fatalf("alert state: %v", err)
	}

	// only build the docker client when enabled; client.New would otherwise fail startup
	var cli *client.Client
	if cfg.Docker.Enabled {
		cli, err = client.New(client.FromEnv)
		if err != nil {
			log.Fatalf("docker: %v", err)
		}
		defer cli.Close()
	}

	startHeartbeat(ctx, cfg.Heartbeat)

	evaluatorEvents := make(chan evaluatorEvent, 100)
	dockerEvents := make(chan DockerEvent, 100)

	ev := newEvaluator(alertManager, cfg, cli)
	// Reconcile persisted docker state before run() starts, while the evaluator
	// still has sole access to its container map.
	if cfg.Docker.Enabled {
		if err := ev.reconcileDockerState(ctx); err != nil {
			log.Fatalf("docker state: %v", err)
		}
	}
	go ev.run(ctx, evaluatorEvents, dockerEvents)

	fmt.Printf("Starting lookout %s on %s (Ctrl+C to stop)\n", version, cfg.Hostname)

	if cfg.Docker.Enabled {
		go dockerCollector(ctx, cli, dockerEvents)
	}

	collector := newMetricCollector(cfg, metricSnapshot, evaluatorEvents)

	ticker := time.NewTicker(cfg.CollectionInterval.Std())
	defer ticker.Stop()

	collector.collectOnce() // collect once at startup instead of waiting a full interval
	for {
		select {
		case <-ctx.Done():
			fmt.Println("Shutting down")
			return
		case <-ticker.C:
			collector.collectOnce()
		}
	}
}

// initNotifiers builds the configured notifiers, falling back to console output
// when none are set, and reports which channels are active.
func initNotifiers(cfg NotifiersConfig) ([]Notifier, error) {
	notifiers, active, err := buildNotifiers(cfg)
	if err != nil {
		return nil, err
	}
	if len(notifiers) == 0 {
		fmt.Println("No notifier configured; alerts will print to the console")
		return []Notifier{&ConsoleNotifier{}}, nil
	}
	fmt.Printf("Active notifiers: %s\n", strings.Join(active, ", "))
	return notifiers, nil
}

// metricCollector runs one full collection pass per tick: it gathers each
// collector's samples, publishes them to the snapshot and the evaluator, and
// threads the per-run state (previous CPU times, disk-fill history) between runs.
type metricCollector struct {
	cfg           Config
	snapshot      *metricsSnapshot
	events        chan<- evaluatorEvent
	cpuPrevious   *cpuTimes
	diskPredictor *diskFillPredictor
}

func newMetricCollector(cfg Config, snapshot *metricsSnapshot, events chan<- evaluatorEvent) *metricCollector {
	return &metricCollector{
		cfg:           cfg,
		snapshot:      snapshot,
		events:        events,
		diskPredictor: newDiskFillPredictor(),
	}
}

func (c *metricCollector) publish(samples []MetricSample) {
	c.snapshot.Update(samples)
	for _, s := range samples {
		c.events <- evaluatorEvent{sample: s, hasSample: true}
	}
}

func (c *metricCollector) emit(label string, samples []MetricSample, err error) {
	if err != nil {
		fmt.Printf("Error collecting %s info: %v\n", label, err)
		return
	}
	c.publish(samples)
}

func (c *metricCollector) collectOnce() {
	memData, err := memoryCollector(procMeminfo)
	c.emit("memory", memData, err)

	diskData, err := diskCollector(procMounts, c.cfg.Alerts.Disk.Mounts)
	if err != nil {
		fmt.Printf("Error collecting disk info: %v\n", err)
	} else {
		c.publish(diskData)
		c.publish(c.diskPredictor.collect(diskData, c.cfg.Alerts.Disk.PredictFullWithin.Std()))
	}

	loadData, err := loadCollector(procLoadavg, runtime.NumCPU())
	c.emit("load", loadData, err)

	// CPU is the one collector that threads state (cpuPrevious) between runs.
	cpuData, nextCPU, err := cpuCollector(procStat, c.cpuPrevious)
	if err != nil {
		fmt.Printf("Error collecting cpu info: %v\n", err)
	} else {
		c.cpuPrevious = nextCPU
		c.publish(cpuData)
	}

	c.publish(systemdCollector(c.cfg.Alerts.Systemd.Services))
	c.publish(httpCollector(c.cfg.Alerts.HTTP.Checks))
	c.publish(tcpCollector(c.cfg.Alerts.TCP.Checks))
	c.publish(processCollector(procDir, c.cfg.Alerts.Process.Names))

	c.events <- evaluatorEvent{checkStale: true}
}

// evaluator is the single goroutine that owns the alert manager and container
// state. Every mutation flows through run()'s channels, so no locking is needed.
type evaluator struct {
	alerts          *AlertManager
	cfg             Config
	cli             *client.Client
	containers      map[string]*ContainerState
	dockerStateFile string
	dockerEvals     chan dockerEvaluation
}

func newEvaluator(alerts *AlertManager, cfg Config, cli *client.Client) *evaluator {
	return &evaluator{
		alerts:          alerts,
		cfg:             cfg,
		cli:             cli,
		containers:      make(map[string]*ContainerState),
		dockerStateFile: dockerStatePath(cfg.StateFile),
		dockerEvals:     make(chan dockerEvaluation, 100),
	}
}

// reconcileDockerState loads persisted container state and reconciles it against
// the containers currently running, dispatching catch-up alerts. It must run
// before run() starts, while the evaluator still has sole access to containers.
func (e *evaluator) reconcileDockerState(ctx context.Context) error {
	containers, err := loadDockerState(e.dockerStateFile)
	if err != nil {
		return err
	}
	e.containers = containers

	snapshots, err := dockerContainerSnapshots(ctx, e.cli, containers)
	if err != nil {
		log.Printf("docker: failed to reconcile running containers: %v", err)
		return nil
	}
	for _, alert := range reconcileDockerAlerts(containers, snapshots, e.cfg.Hostname, e.cfg.Docker) {
		e.alerts.dispatch(alert)
	}
	e.persistDockerState()
	return nil
}

func (e *evaluator) persistDockerState() {
	if !e.cfg.Docker.Enabled {
		return
	}
	if err := saveDockerState(e.dockerStateFile, e.containers); err != nil {
		log.Printf("error saving docker state: %v", err)
	}
}

func (e *evaluator) run(ctx context.Context, samples <-chan evaluatorEvent, dockerEvents <-chan DockerEvent) {
	for {
		select {
		case event, ok := <-samples:
			if !ok {
				return
			}
			if event.hasSample {
				e.alerts.Evaluate(event.sample)
			}
			if event.checkStale {
				e.alerts.CheckStale()
			}

		case dockerEvent, ok := <-dockerEvents:
			if !ok {
				return
			}
			if alerts := handleDockerEvent(e.containers, dockerEvent, e.dockerEvals, e.cfg.Hostname, e.cfg.Docker); len(alerts) > 0 {
				for _, alert := range alerts {
					e.alerts.dispatch(alert)
				}
				e.persistDockerState()
			}

		case pending, ok := <-e.dockerEvals:
			if !ok {
				continue
			}
			if container := e.containers[pending.ID]; container != nil {
				var alert *Alert
				switch pending.Kind {
				case "restart":
					alert = evaluateDockerRestartLoop(container, e.cfg.Hostname, e.cfg.Docker, time.Now())
				default:
					alert = evaluateContainer(container, e.cfg.Hostname, e.cfg.Docker)
				}
				if alert != nil {
					e.alerts.dispatch(*alert)
					e.persistDockerState()
				}
			}
		}
	}
}

// ruleSpec pairs an alert rule with the metric names tracked under its ID for
// staleness. Declaring both together is what keeps buildRules and trackedMetrics
// from drifting: every rule names its tracked metrics in exactly one place.
type ruleSpec struct {
	rule    Rule
	tracked []string
}

func buildRuleSpecs(cfg Config) []ruleSpec {
	specs := []ruleSpec{
		{
			rule: Rule{
				ID:           "memory",
				Matcher:      func(s MetricSample) bool { return s.Name == "memory.used_percent" },
				Threshold:    cfg.Alerts.Memory.Threshold,
				ResolveBelow: *cfg.Alerts.Memory.ResolveBelow,
				Message:      "High memory usage",
				Severity:     cfg.Alerts.Memory.Severity,
				For:          cfg.Alerts.Memory.For.Std(),
			},
			tracked: []string{"memory.used_percent"},
		},
		{
			rule: Rule{
				ID:           "disk",
				Matcher:      func(s MetricSample) bool { return s.Collector == "disk" && strings.HasSuffix(s.Name, ".used_percent") },
				Threshold:    cfg.Alerts.Disk.Threshold,
				ResolveBelow: *cfg.Alerts.Disk.ResolveBelow,
				Message:      "High disk usage",
				Severity:     cfg.Alerts.Disk.Severity,
				For:          cfg.Alerts.Disk.For.Std(),
			},
			tracked: diskMetricNames(cfg, ".used_percent"),
		},
		{
			rule: Rule{
				ID:           "load",
				Matcher:      func(s MetricSample) bool { return s.Name == "load.1_per_core" },
				Threshold:    cfg.Alerts.Load.Threshold,
				ResolveBelow: *cfg.Alerts.Load.ResolveBelow,
				Message:      "High 1-minute load per core",
				Severity:     cfg.Alerts.Load.Severity,
				For:          cfg.Alerts.Load.For.Std(),
			},
			tracked: []string{"load.1_per_core"},
		},
		{
			rule: Rule{
				ID:           "cpu",
				Matcher:      func(s MetricSample) bool { return s.Name == "cpu.used_percent" },
				Threshold:    cfg.Alerts.CPU.Threshold,
				ResolveBelow: *cfg.Alerts.CPU.ResolveBelow,
				Message:      "High CPU usage",
				Severity:     cfg.Alerts.CPU.Severity,
				For:          cfg.Alerts.CPU.For.Std(),
			},
			tracked: []string{"cpu.used_percent"},
		},
		{
			rule: Rule{
				ID:           "swap",
				Matcher:      func(s MetricSample) bool { return s.Name == "swap.used_percent" },
				Threshold:    cfg.Alerts.Swap.Threshold,
				ResolveBelow: *cfg.Alerts.Swap.ResolveBelow,
				Message:      "High swap usage",
				Severity:     cfg.Alerts.Swap.Severity,
				For:          cfg.Alerts.Swap.For.Std(),
			},
			tracked: []string{"swap.used_percent"},
		},
	}

	if cfg.Alerts.Disk.PredictFullWithin.Std() > 0 {
		specs = append(specs, ruleSpec{
			rule: Rule{
				ID: "disk-fill",
				Matcher: func(s MetricSample) bool {
					return s.Collector == "disk" && strings.HasSuffix(s.Name, ".fills_within_window")
				},
				Threshold:    0,
				ResolveBelow: 0,
				Message:      "Disk predicted to fill soon",
				Severity:     cfg.Alerts.Disk.Severity,
				For:          cfg.Alerts.Disk.For.Std(),
			},
			tracked: diskMetricNames(cfg, ".fills_within_window"),
		})
	}

	for _, family := range checkFamilies(cfg) {
		for _, dc := range family.derived() {
			metricName := dc.metricName
			specs = append(specs, ruleSpec{
				rule: Rule{
					ID:       dc.ruleID,
					Matcher:  func(s MetricSample) bool { return s.Name == metricName },
					Message:  family.message + dc.label,
					Severity: family.severity,
				},
				tracked: []string{dc.metricName},
			})
		}
	}
	return specs
}

// diskMetricNames returns the metric name for each configured mount, e.g.
// "disk.root.used_percent". It mirrors the names the disk collector emits.
func diskMetricNames(cfg Config, suffix string) []string {
	names := make([]string, 0, len(cfg.Alerts.Disk.Mounts))
	for _, mount := range cfg.Alerts.Disk.Mounts {
		names = append(names, "disk."+mountPointToName(mount)+suffix)
	}
	return names
}

func buildRules(cfg Config) []Rule {
	specs := buildRuleSpecs(cfg)
	rules := make([]Rule, 0, len(specs))
	for _, spec := range specs {
		rules = append(rules, spec.rule)
	}
	return rules
}

func startHeartbeat(ctx context.Context, cfg HeartbeatConfig) {
	if cfg.URL == "" {
		return
	}
	go func() {
		if err := PingRemote(cfg.URL); err != nil {
			fmt.Printf("Initial heartbeat failed: %v\n", err)
		}

		ticker := time.NewTicker(cfg.Interval.Std())
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := PingRemote(cfg.URL); err != nil {
					fmt.Printf("Heartbeat failed: %v\n", err)
				}
			}
		}
	}()
}

func trackedMetrics(cfg Config) []TrackedMetric {
	var tracked []TrackedMetric
	for _, spec := range buildRuleSpecs(cfg) {
		for _, name := range spec.tracked {
			tracked = append(tracked, TrackedMetric{RuleID: spec.rule.ID, Name: name})
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
