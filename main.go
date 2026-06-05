package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/moby/moby/client"
	"golang.org/x/sys/unix"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

type MetricSample struct {
	Name      string
	Value     float64
	Unit      string
	Timestamp time.Time
	Collector string
}

type DockerEvent struct {
	ID         string
	Timestamp  time.Time
	Action     string
	Attributes map[string]string // this will have info about say the image, exit code
}

// type Collector interface {
// 	Collect(ctx context.Context) ([]MetricSample, error)
// }

func memoryCollector(path string) ([]MetricSample, error) {
	memInfo := make(map[string]float64)
	data, err := os.ReadFile(path)
	if err != nil {
		return []MetricSample{}, errors.New("There was an issue reading the /proc/memory file")
	}

	// proc/memory has key value pairs, separated by : and the values have KB
	parts := strings.SplitSeq(string(data), "\n")
	// convert the data into a map of key value pairs and then we read each of those key value stuff
	for part := range parts {
		if part == "" {
			continue
		}
		keyValueSplit := strings.SplitN(part, ":", 2)

		if len(keyValueSplit) != 2 {
			continue
		}

		key := strings.TrimSpace(keyValueSplit[0])
		value, err := strconv.Atoi(
			strings.TrimSpace(strings.Split(strings.TrimSpace(keyValueSplit[1]), " ")[0]),
		)
		if err != nil {
			fmt.Println("Something went wrong")
		}

		memInfo[key] = float64(value)

	}

	timeCollected := time.Now()
	memUsed := memInfo["MemTotal"] - memInfo["MemAvailable"]

	var metricsCollected []MetricSample

	metricsCollected = append(metricsCollected, MetricSample{
		Name:      "memory.total",
		Value:     memInfo["MemTotal"],
		Unit:      "kB",
		Timestamp: timeCollected,
		Collector: "memory",
	})

	metricsCollected = append(metricsCollected, MetricSample{
		Name:      "memory.used_percent",
		Value:     (memUsed / memInfo["MemTotal"]) * 100,
		Unit:      "percent",
		Timestamp: timeCollected,
		Collector: "memory",
	})

	metricsCollected = append(metricsCollected, MetricSample{
		Name:      "memory.available",
		Value:     memInfo["MemAvailable"],
		Unit:      "kB",
		Timestamp: timeCollected,
		Collector: "memory",
	})

	metricsCollected = append(metricsCollected, MetricSample{
		Name:      "memory.used",
		Value:     memUsed,
		Unit:      "kB",
		Timestamp: timeCollected,
		Collector: "memory",
	})

	return metricsCollected, nil
}

func dockerCollector(cli *client.Client, ctx context.Context, dockerEventsChannel chan<- DockerEvent) {
	f := make(client.Filters).Add("type", "container")
	fmt.Println("Listening to Docker container events...")

	for ctx.Err() == nil {
		err := listenDockerEvents(cli, ctx, f, dockerEventsChannel)
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Printf("[docker] event stream error: %v\n", err)
		}

		// we are listening to the events from the docker daemon, the reason we are
		// sleeping here is so if there's any issue with the daemon, we try again
		// after some time in order to resume listening to the events
		// we will be looking at a better way to do this though.
		// issue here is after sleeping for 2 seconds, assuming there were events
		// that happened during that time, we will miss them all. so we need to always
		// start checking / listening to events for every 2 seconds before.
		time.Sleep(2 * time.Second)
	}
}

func diskCollector(path string, targetMounts []string) ([]MetricSample, error) {
	timeCollected := time.Now()
	var metricsCollected []MetricSample

	mountsData, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}

	//bool is bigger than empty struct
	targetSet := make(map[string]bool, len(targetMounts))
	for _, m := range targetMounts {
		targetSet[m] = true
	}

	lines := strings.Split(strings.TrimSpace(string(mountsData)), "\n")
	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		mountPoint := fields[1]

		if len(targetMounts) > 0 && !targetSet[mountPoint] {
			continue
		}

		var stat unix.Statfs_t
		if err := unix.Statfs(mountPoint, &stat); err != nil {
			continue
		}

		if stat.Bsize <= 0 || stat.Blocks == 0 {
			continue
		}

		blockSize := uint64(stat.Bsize)
		total := stat.Blocks * blockSize
		free := stat.Bfree * blockSize
		used := total - free
		usedPercent := (float64(used) / float64(total)) * 100

		name := mountPointToName(mountPoint)
		metricsCollected = append(metricsCollected,
			MetricSample{Name: "disk." + name + ".total", Value: float64(total), Unit: "bytes", Timestamp: timeCollected, Collector: "disk"},
			MetricSample{Name: "disk." + name + ".used", Value: float64(used), Unit: "bytes", Timestamp: timeCollected, Collector: "disk"},
			MetricSample{Name: "disk." + name + ".free", Value: float64(free), Unit: "bytes", Timestamp: timeCollected, Collector: "disk"},
			MetricSample{Name: "disk." + name + ".used_percent", Value: usedPercent, Unit: "percent", Timestamp: timeCollected, Collector: "disk"},
		)
	}

	return metricsCollected, nil
}

func mountPointToName(mp string) string {
	if mp == "/" {
		return "root"
	}
	return strings.ReplaceAll(strings.TrimPrefix(mp, "/"), "/", "_")
}

type State string

const (
	Running State = "running"
	Exited  State = "stopped"
	Paused  State = "paused"
	Created State = "created"
	Removed State = "removed"
)

type ContainerState struct {
	ID         string
	Name       string
	Image      string
	State      State
	LastOOMAt  time.Time
	LastExit   int
	LastStopAt time.Time
	LastDieAt  time.Time
	PendingDie bool
	StartedAt  time.Time
	DieTimes   []time.Time
}

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

	evaluateContainer := func(c *ContainerState) {
		if c.PendingDie {
			if !c.LastOOMAt.IsZero() {
				// died with an OOM
				fmt.Printf("the container %s died with an OOM, immediate alert", c.ID)
			} else if !c.LastStopAt.IsZero() && c.LastExit != 0 {
				fmt.Printf("the container %s did not die cleanly", c.ID)
			}

			if c.LastExit != 0 {
				// the container did not die cleanly
				fmt.Printf("The container did not die cleanly")
			}
		}

		c.PendingDie = false
		c.LastOOMAt = time.Time{}
		c.LastStopAt = time.Time{}
	}

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

				container := containers[dockerEvent.ID]
				if container == nil {
					container = &ContainerState{
						ID: dockerEvent.ID,
					}
					containers[dockerEvent.ID] = container
				}

				if name := dockerEvent.Attributes["name"]; name != "" {
					container.Name = name
				}
				if image := dockerEvent.Attributes["image"]; image != "" {
					container.Image = image
				}

				switch dockerEvent.Action {
				case "start", "restart":
					container.State = Running
					container.StartedAt = dockerEvent.Timestamp
					// container.HasOOM, container.HasStopped, container.PendingDie = false, false, false
					// evaluateContainer(container)

					if container.PendingDie && container.LastDieAt.Before(container.StartedAt) {
						// we should mark as resolved
						container.PendingDie = false
						// TODO: evaluate the restart loop here
					}
				case "die":
					container.State = Exited
					if code, err := strconv.Atoi(dockerEvent.Attributes["exitCode"]); err == nil {
						container.LastExit = code
					}
					container.LastDieAt = dockerEvent.Timestamp
					container.DieTimes = append(container.DieTimes, dockerEvent.Timestamp)
					container.PendingDie = true

					// a way to trigger an evaluation after X seconds after the die has happened
					id := dockerEvent.ID
					time.AfterFunc(1500*time.Millisecond, func() {
						dockerEventsEvaluationChannel <- id
					})
				case "stop":
					container.LastStopAt = dockerEvent.Timestamp
				case "oom":
					container.LastOOMAt = dockerEvent.Timestamp
					// FIX: we will have the correct alerts for this later
					fmt.Printf("The container %s was killed because of an OOM", container.ID)
				}

			case pendingContainerToEvaluate, ok := <-dockerEventsEvaluationChannel:
				if !ok {
					continue
				}
				container := containers[pendingContainerToEvaluate]
				if container == nil {
					continue
				}
				evaluateContainer(container)
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
