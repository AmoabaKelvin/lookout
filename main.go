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

	// Cancel the context on SIGINT/SIGTERM so the agent shuts down cleanly
	// (important when running under systemd, which stops services with SIGTERM).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Pick notifiers based on which webhook URLs are configured. With none set,
	// fall back to the console so the agent is still useful with zero config.
	var notifiers []Notifier
	if cfg.GoogleChatWebhookURL != "" {
		notifiers = append(notifiers, &GoogleChatNotifier{WebhookURL: cfg.GoogleChatWebhookURL})
	}
	if cfg.DiscordWebhookURL != "" {
		notifiers = append(notifiers, &DiscordNotifier{WebhookURL: cfg.DiscordWebhookURL})
	}
	if len(notifiers) == 0 {
		notifiers = append(notifiers, &ConsoleNotifier{})
		fmt.Println("No webhook configured; alerts will print to the console")
	}

	// Threshold rules. The user-facing config (thresholds) is translated into
	// these internal rules once, at startup.
	rules := []Rule{
		{
			Matcher:   func(s MetricSample) bool { return s.Name == "memory.used_percent" },
			Threshold: cfg.MemThreshold,
			Message:   "High memory usage",
		},
		{
			Matcher:   func(s MetricSample) bool { return s.Collector == "disk" && strings.HasSuffix(s.Name, ".used_percent") },
			Threshold: cfg.DiskThreshold,
			Message:   "High disk usage",
		},
	}

	alertManager := NewAlertManager(rules, cfg.RenotifyAfter, cfg.Hostname, notifiers)

	// Docker is optional and off by default. Only create the client (a hard
	// dependency that would otherwise fail startup) when explicitly enabled.
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

	// this is the evaluator that will be receiving events and processing them
	// this is going to be stateful because some things need state in order
	// for us to make good decisions. example. we need to keep track of the
	// docker events and debounce them before we send stuff out to the alert manager
	// we don't just fan things out
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
