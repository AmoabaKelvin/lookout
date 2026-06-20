package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
)

type DockerEvent struct {
	ID         string
	Timestamp  time.Time
	Action     string
	Attributes map[string]string
}

type dockerEvaluation struct {
	ID   string
	Kind string
}

type dockerSnapshot struct {
	Running bool
	Health  string
}

type State string

const (
	Running State = "running"
	Exited  State = "stopped"
	Removed State = "removed"
)

// dieDebounce is how long a die evaluation waits before deciding a container
// has truly failed, so a quick restart can cancel the pending failure.
const dieDebounce = 1500 * time.Millisecond

// healthUnhealthy is Docker's container health status for a failing healthcheck.
const healthUnhealthy = "unhealthy"

type ContainerState struct {
	ID              string
	Name            string
	Image           string
	State           State
	LastOOMAt       time.Time
	LastExit        int
	LastDieAt       time.Time
	PendingDie      bool
	IntentionalStop bool
	StartedAt       time.Time
	StartTimes      []time.Time
	IsAlerting      bool
	RestartAlerting bool
	HealthAlerting  bool
}

type storedDockerState struct {
	Containers []storedContainerState `json:"containers"`
}

type storedContainerState struct {
	ID              string `json:"id"`
	Name            string `json:"name,omitempty"`
	Image           string `json:"image,omitempty"`
	LastExit        int    `json:"last_exit,omitempty"`
	IsAlerting      bool   `json:"is_alerting"`
	RestartAlerting bool   `json:"restart_alerting,omitempty"`
	HealthAlerting  bool   `json:"health_alerting,omitempty"`
}

func dockerCollector(ctx context.Context, cli *client.Client, events chan<- DockerEvent) {
	f := make(client.Filters).Add("type", "container")
	fmt.Println("Listening to Docker container events...")

	var lastSeen time.Time
	for ctx.Err() == nil {
		latest, err := listenDockerEvents(ctx, cli, f, lastSeen, events)
		if latest.After(lastSeen) {
			lastSeen = latest
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Printf("[docker] event stream error: %v\n", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func dockerContainerSnapshots(ctx context.Context, cli *client.Client, containers map[string]*ContainerState) (map[string]dockerSnapshot, error) {
	result, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, err
	}

	snapshots := make(map[string]dockerSnapshot, len(result.Items))
	for _, container := range result.Items {
		snapshots[container.ID] = dockerSnapshot{Running: string(container.State) == string(Running)}
	}

	for id, container := range containers {
		if !container.HealthAlerting {
			continue
		}
		snapshot, ok := snapshots[id]
		if !ok {
			continue
		}
		inspect, err := cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil || inspect.Container.State == nil || inspect.Container.State.Health == nil {
			continue
		}
		snapshot.Health = string(inspect.Container.State.Health.Status)
		snapshots[id] = snapshot
	}
	return snapshots, nil
}

func listenDockerEvents(ctx context.Context, cli *client.Client, f client.Filters, since time.Time, out chan<- DockerEvent) (time.Time, error) {
	options := client.EventsListOptions{Filters: f}
	if !since.IsZero() {
		options.Since = dockerEventSince(since)
	}
	result := cli.Events(ctx, options)
	latest := since

	for {
		select {
		case <-ctx.Done():
			return latest, ctx.Err()

		case event, ok := <-result.Messages:
			if !ok {
				return latest, io.EOF
			}

			eventTime := dockerMessageTime(event)
			if !eventTime.IsZero() {
				if !since.IsZero() && !eventTime.After(since) {
					continue
				}
				if eventTime.After(latest) {
					latest = eventTime
				}
			}

			name := event.Actor.Attributes["name"]
			timestamp := "unknown"
			if !eventTime.IsZero() {
				timestamp = eventTime.Format("15:04:05")
			}
			fmt.Printf("[docker] [%s] %s\n", timestamp, name)

			switch event.Action {
			case events.ActionDie, events.ActionDestroy, events.ActionHealthStatusHealthy, events.ActionHealthStatusUnhealthy, events.ActionOOM, events.ActionRemove, events.ActionRestart, events.ActionStart, events.ActionStop:
				out <- DockerEvent{
					ID:         event.Actor.ID,
					Timestamp:  eventTime,
					Action:     string(event.Action),
					Attributes: event.Actor.Attributes,
				}
			}

		case err, ok := <-result.Err:
			if !ok || err == nil {
				return latest, io.EOF
			}
			return latest, err
		}
	}
}

func dockerMessageTime(event events.Message) time.Time {
	if event.TimeNano > 0 {
		return time.Unix(0, event.TimeNano)
	}
	if event.Time > 0 {
		return time.Unix(event.Time, 0)
	}
	return time.Time{}
}

func dockerEventSince(t time.Time) string {
	return t.Format("2006-01-02T15:04:05.999999999Z07:00")
}

func dockerStatePath(stateFile string) string {
	if stateFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(stateFile), "docker-state.json")
}

func loadDockerState(path string) (map[string]*ContainerState, error) {
	containers := make(map[string]*ContainerState)
	if path == "" {
		return containers, nil
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return containers, nil
	}
	if err != nil {
		return nil, err
	}

	var stored storedDockerState
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, err
	}
	for _, c := range stored.Containers {
		if c.ID == "" || (!c.IsAlerting && !c.RestartAlerting && !c.HealthAlerting) {
			continue
		}
		containers[c.ID] = &ContainerState{
			ID:              c.ID,
			Name:            c.Name,
			Image:           c.Image,
			LastExit:        c.LastExit,
			IsAlerting:      c.IsAlerting,
			RestartAlerting: c.RestartAlerting,
			HealthAlerting:  c.HealthAlerting,
		}
	}
	return containers, nil
}

func saveDockerState(path string, containers map[string]*ContainerState) error {
	if path == "" {
		return nil
	}

	stored := storedDockerState{Containers: []storedContainerState{}}
	for _, c := range containers {
		if !c.IsAlerting && !c.RestartAlerting && !c.HealthAlerting {
			continue
		}
		stored.Containers = append(stored.Containers, storedContainerState{
			ID:              c.ID,
			Name:            c.Name,
			Image:           c.Image,
			LastExit:        c.LastExit,
			IsAlerting:      c.IsAlerting,
			RestartAlerting: c.RestartAlerting,
			HealthAlerting:  c.HealthAlerting,
		})
	}

	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".docker-state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func reconcileDockerAlerts(containers map[string]*ContainerState, snapshots map[string]dockerSnapshot, hostname string, cfg DockerConfig) []Alert {
	var alerts []Alert
	for id, container := range containers {
		snapshot, exists := snapshots[id]
		if !exists {
			alerts = append(alerts, dockerRemovalAlerts(container, hostname, cfg.Severity, cfg.RestartThreshold)...)
			delete(containers, id)
			continue
		}
		if container.IsAlerting && snapshot.Running {
			container.IsAlerting = false
			alerts = append(alerts, *dockerExitAlert(container, hostname, cfg.Severity, false, "Container running"))
		}
		if container.RestartAlerting {
			container.RestartAlerting = false
			alerts = append(alerts, *dockerRestartAlert(container, hostname, cfg.Severity, false, cfg.RestartThreshold))
		}
		if container.HealthAlerting && snapshot.Health != healthUnhealthy {
			container.HealthAlerting = false
			alerts = append(alerts, *dockerHealthAlert(container, hostname, cfg.Severity, false))
		}
	}
	return alerts
}

// handleDockerEvent updates container state from one event. A die is debounced
// via evalCh so a quick restart can cancel the pending failure evaluation.
func handleDockerEvent(containers map[string]*ContainerState, dockerEvent DockerEvent, evalCh chan<- dockerEvaluation, hostname string, cfg DockerConfig) []Alert {
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
		container.IntentionalStop = false
		container.StartTimes = appendRecentTimes(container.StartTimes, dockerEvent.Timestamp, cfg.RestartWindow.Std())

		if container.PendingDie && !container.StartedAt.Before(container.LastDieAt) {
			container.PendingDie = false
		}
		if container.IsAlerting {
			container.IsAlerting = false
			return []Alert{*dockerExitAlert(container, hostname, cfg.Severity, false, "Container running")}
		}
		if len(container.StartTimes) >= cfg.RestartThreshold && !container.RestartAlerting {
			container.RestartAlerting = true
			id := dockerEvent.ID
			window := cfg.RestartWindow.Std()
			time.AfterFunc(window, func() {
				evalCh <- dockerEvaluation{ID: id, Kind: "restart"}
			})
			return []Alert{*dockerRestartAlert(container, hostname, cfg.Severity, true, cfg.RestartThreshold)}
		}
	case "destroy", "remove":
		container.State = Removed
		alerts := dockerRemovalAlerts(container, hostname, cfg.Severity, cfg.RestartThreshold)
		delete(containers, dockerEvent.ID)
		return alerts
	case "die":
		container.State = Exited
		if code, err := strconv.Atoi(dockerEvent.Attributes["exitCode"]); err == nil {
			container.LastExit = code
		}
		container.LastDieAt = dockerEvent.Timestamp
		container.PendingDie = true

		id := dockerEvent.ID
		time.AfterFunc(dieDebounce, func() {
			evalCh <- dockerEvaluation{ID: id, Kind: "die"}
		})
	case "oom":
		container.LastOOMAt = dockerEvent.Timestamp
	case "stop":
		// An operator stopped the container; the die that follows is expected.
		container.IntentionalStop = true
	case string(events.ActionHealthStatusUnhealthy):
		if !container.HealthAlerting {
			container.HealthAlerting = true
			return []Alert{*dockerHealthAlert(container, hostname, cfg.Severity, true)}
		}
	case string(events.ActionHealthStatusHealthy):
		if container.HealthAlerting {
			container.HealthAlerting = false
			return []Alert{*dockerHealthAlert(container, hostname, cfg.Severity, false)}
		}
	}
	return nil
}

func evaluateContainer(c *ContainerState, hostname string, cfg DockerConfig) *Alert {
	defer func() {
		c.PendingDie = false
		c.LastOOMAt = time.Time{}
		c.IntentionalStop = false
	}()

	if !c.PendingDie {
		return nil
	}
	if c.State == Running {
		return nil
	}
	// A "stop" event means an operator stopped the container, so a non-zero exit
	// (e.g. 143 from SIGTERM) is expected shutdown, not a failure.
	if c.IntentionalStop {
		return nil
	}
	if c.LastExit == 0 && c.LastOOMAt.IsZero() {
		return nil
	}
	if c.IsAlerting {
		return nil
	}

	c.IsAlerting = true
	title := "Container exited with non-zero status"
	if !c.LastOOMAt.IsZero() {
		title = "Container killed by OOM"
	}
	return dockerExitAlert(c, hostname, cfg.Severity, true, title)
}

func evaluateDockerRestartLoop(c *ContainerState, hostname string, cfg DockerConfig, now time.Time) *Alert {
	// Re-check the window without recording a start: if recent restarts have
	// aged out and dropped below the threshold, the loop has subsided.
	c.StartTimes = recentTimes(c.StartTimes, now, cfg.RestartWindow.Std())
	if !c.RestartAlerting || len(c.StartTimes) >= cfg.RestartThreshold {
		return nil
	}
	c.RestartAlerting = false
	return dockerRestartAlert(c, hostname, cfg.Severity, false, cfg.RestartThreshold)
}

func dockerRemovalAlerts(c *ContainerState, hostname string, severity Severity, restartThreshold int) []Alert {
	var alerts []Alert
	if c.IsAlerting {
		c.IsAlerting = false
		alerts = append(alerts, *dockerExitAlert(c, hostname, severity, false, "Container removed"))
	}
	if c.RestartAlerting {
		c.RestartAlerting = false
		alerts = append(alerts, *dockerRestartAlert(c, hostname, severity, false, restartThreshold))
	}
	if c.HealthAlerting {
		c.HealthAlerting = false
		alerts = append(alerts, *dockerHealthAlert(c, hostname, severity, false))
	}
	return alerts
}

func dockerExitAlert(c *ContainerState, hostname string, severity Severity, firing bool, title string) *Alert {
	value := float64(0)
	if firing {
		value = float64(c.LastExit)
	}
	return &Alert{
		IsFiring:  firing,
		Title:     title,
		Value:     value,
		Threshold: 0,
		Hostname:  hostname,
		Metric:    "docker.container." + dockerMetricName(c) + ".exit_code",
		Unit:      "exit_code",
		Severity:  severity,
	}
}

func dockerRestartAlert(c *ContainerState, hostname string, severity Severity, firing bool, threshold int) *Alert {
	return &Alert{
		IsFiring:  firing,
		Title:     "Container restart loop",
		Value:     float64(len(c.StartTimes)),
		Threshold: float64(threshold),
		Hostname:  hostname,
		Metric:    "docker.container." + dockerMetricName(c) + ".restarts",
		Unit:      "count",
		Severity:  severity,
	}
}

func dockerHealthAlert(c *ContainerState, hostname string, severity Severity, firing bool) *Alert {
	title := "Container healthcheck unhealthy"
	value := float64(1)
	if !firing {
		title = "Container healthcheck healthy"
		value = 0
	}
	return &Alert{
		IsFiring:  firing,
		Title:     title,
		Value:     value,
		Threshold: 0,
		Hostname:  hostname,
		Metric:    "docker.container." + dockerMetricName(c) + ".health",
		Unit:      "state",
		Severity:  severity,
	}
}

// recentTimes drops entries older than window before now. window <= 0 keeps all.
func recentTimes(times []time.Time, now time.Time, window time.Duration) []time.Time {
	if window <= 0 {
		return times
	}
	cutoff := now.Add(-window)
	kept := times[:0]
	for _, existing := range times {
		if !existing.Before(cutoff) {
			kept = append(kept, existing)
		}
	}
	return kept
}

// appendRecentTimes records t and drops entries older than window before t.
func appendRecentTimes(times []time.Time, t time.Time, window time.Duration) []time.Time {
	if t.IsZero() {
		t = time.Now()
	}
	return append(recentTimes(times, t, window), t)
}

func dockerMetricName(c *ContainerState) string {
	name := c.Name
	if name == "" {
		name = c.ID
	}
	name = strings.TrimPrefix(name, "/")
	name = safeMetricPart(name)
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}
