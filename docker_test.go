package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func dockerEvalChannel() chan dockerEvaluation {
	return make(chan dockerEvaluation, 1)
}

func oneDockerAlert(t *testing.T, alerts []Alert) Alert {
	t.Helper()
	if len(alerts) != 1 {
		t.Fatalf("alerts = %d, want 1: %+v", len(alerts), alerts)
	}
	return alerts[0]
}

func testDockerConfig() DockerConfig {
	return DockerConfig{
		Enabled:          true,
		Severity:         SeverityCritical,
		RestartThreshold: 3,
		RestartWindow:    Duration(10 * time.Minute),
	}
}

func dockerDieEvent(id, name, exitCode string) DockerEvent {
	return DockerEvent{
		ID:        id,
		Timestamp: time.Unix(100, 0),
		Action:    "die",
		Attributes: map[string]string{
			"name":     name,
			"exitCode": exitCode,
		},
	}
}

func TestDockerNonZeroExitBuildsAlert(t *testing.T) {
	containers := map[string]*ContainerState{}
	event := dockerDieEvent("abcdef123456", "api.1", "42")

	cfg := testDockerConfig()
	handleDockerEvent(containers, event, dockerEvalChannel(), "host-a", cfg)
	alert := evaluateContainer(containers[event.ID], "host-a", cfg)

	if alert == nil {
		t.Fatal("expected docker alert")
	}
	if !alert.IsFiring {
		t.Fatalf("expected firing alert, got %+v", alert)
	}
	if alert.Title != "Container exited with non-zero status" {
		t.Fatalf("title = %q", alert.Title)
	}
	if alert.Value != 42 {
		t.Fatalf("value = %v, want exit code 42", alert.Value)
	}
	if alert.Metric != "docker.container.api_1.exit_code" {
		t.Fatalf("metric = %q", alert.Metric)
	}
}

func TestDockerCleanExitDoesNotAlert(t *testing.T) {
	containers := map[string]*ContainerState{}
	event := dockerDieEvent("abcdef123456", "worker", "0")

	cfg := testDockerConfig()
	handleDockerEvent(containers, event, dockerEvalChannel(), "host-a", cfg)
	alert := evaluateContainer(containers[event.ID], "host-a", cfg)

	if alert != nil {
		t.Fatalf("expected no alert for clean exit, got %+v", alert)
	}
}

func TestDockerQuickRestartSuppressesExitAlert(t *testing.T) {
	containers := map[string]*ContainerState{}
	cfg := testDockerConfig()
	evalCh := dockerEvalChannel()

	handleDockerEvent(containers, dockerDieEvent("abcdef123456", "api", "255"), evalCh, "host-a", cfg)
	alerts := handleDockerEvent(containers, DockerEvent{
		ID:         "abcdef123456",
		Timestamp:  time.Unix(100, 0),
		Action:     "start",
		Attributes: map[string]string{"name": "api"},
	}, evalCh, "host-a", cfg)
	if len(alerts) > 0 {
		t.Fatalf("quick restart should not resolve or fire an exit alert, got %+v", alerts)
	}

	if alert := evaluateContainer(containers["abcdef123456"], "host-a", cfg); alert != nil {
		t.Fatalf("quick restart should suppress delayed exit alert, got %+v", alert)
	}
}

func TestDockerRestartWithinDebounceSuppressesExitAlert(t *testing.T) {
	containers := map[string]*ContainerState{}
	cfg := testDockerConfig()
	evalCh := dockerEvalChannel()

	handleDockerEvent(containers, dockerDieEvent("abcdef123456", "api", "255"), evalCh, "host-a", cfg)
	alerts := handleDockerEvent(containers, DockerEvent{
		ID:         "abcdef123456",
		Timestamp:  time.Unix(110, 0),
		Action:     "start",
		Attributes: map[string]string{"name": "api"},
	}, evalCh, "host-a", cfg)
	if len(alerts) > 0 {
		t.Fatalf("restart within debounce should not resolve or fire an exit alert, got %+v", alerts)
	}

	if alert := evaluateContainer(containers["abcdef123456"], "host-a", cfg); alert != nil {
		t.Fatalf("restart within debounce should suppress delayed exit alert, got %+v", alert)
	}
}

func TestDockerRunningContainerDoesNotFireStalePendingDie(t *testing.T) {
	cfg := testDockerConfig()
	container := &ContainerState{
		ID:         "abcdef123456",
		Name:       "api",
		State:      Running,
		LastExit:   255,
		PendingDie: true,
	}

	if alert := evaluateContainer(container, "host-a", cfg); alert != nil {
		t.Fatalf("running container should not fire stale pending die alert, got %+v", alert)
	}
}

func TestDockerOOMBuildsAlert(t *testing.T) {
	containers := map[string]*ContainerState{}
	event := dockerDieEvent("abcdef123456", "worker", "137")
	cfg := testDockerConfig()
	handleDockerEvent(containers, event, dockerEvalChannel(), "host-a", cfg)
	handleDockerEvent(containers, DockerEvent{
		ID:         event.ID,
		Timestamp:  time.Unix(101, 0),
		Action:     "oom",
		Attributes: map[string]string{"name": "worker"},
	}, dockerEvalChannel(), "host-a", cfg)

	alert := evaluateContainer(containers[event.ID], "host-a", cfg)

	if alert == nil {
		t.Fatal("expected docker alert")
	}
	if alert.Title != "Container killed by OOM" {
		t.Fatalf("title = %q", alert.Title)
	}
}

// A graceful `docker stop` makes the container exit non-zero (e.g. 143 from
// SIGTERM) and emits a "stop" event after the "die". That is expected shutdown,
// not a failure, so it must not alert.
func TestDockerIntentionalStopDoesNotAlert(t *testing.T) {
	containers := map[string]*ContainerState{}
	cfg := testDockerConfig()

	handleDockerEvent(containers, dockerDieEvent("abcdef123456", "api", "143"), dockerEvalChannel(), "host-a", cfg)
	handleDockerEvent(containers, DockerEvent{
		ID:         "abcdef123456",
		Timestamp:  time.Unix(101, 0),
		Action:     "stop",
		Attributes: map[string]string{"name": "api"},
	}, dockerEvalChannel(), "host-a", cfg)

	if alert := evaluateContainer(containers["abcdef123456"], "host-a", cfg); alert != nil {
		t.Fatalf("intentional stop should not alert, got %+v", alert)
	}
}

// After an intentional stop is suppressed, the container starting and then
// crashing for real must still alert (the stop flag must not linger).
func TestDockerCrashAfterStopAndRestartStillAlerts(t *testing.T) {
	containers := map[string]*ContainerState{}
	cfg := testDockerConfig()

	handleDockerEvent(containers, dockerDieEvent("abcdef123456", "api", "143"), dockerEvalChannel(), "host-a", cfg)
	handleDockerEvent(containers, DockerEvent{ID: "abcdef123456", Timestamp: time.Unix(101, 0), Action: "stop", Attributes: map[string]string{"name": "api"}}, dockerEvalChannel(), "host-a", cfg)
	if alert := evaluateContainer(containers["abcdef123456"], "host-a", cfg); alert != nil {
		t.Fatalf("intentional stop should not alert, got %+v", alert)
	}

	handleDockerEvent(containers, DockerEvent{ID: "abcdef123456", Timestamp: time.Unix(110, 0), Action: "start", Attributes: map[string]string{"name": "api"}}, dockerEvalChannel(), "host-a", cfg)
	handleDockerEvent(containers, DockerEvent{ID: "abcdef123456", Timestamp: time.Unix(120, 0), Action: "die", Attributes: map[string]string{"name": "api", "exitCode": "1"}}, dockerEvalChannel(), "host-a", cfg)

	alert := evaluateContainer(containers["abcdef123456"], "host-a", cfg)
	if alert == nil || !alert.IsFiring {
		t.Fatalf("a real crash after restart should alert, got %+v", alert)
	}
}

func TestDockerStartResolvesActiveAlert(t *testing.T) {
	containers := map[string]*ContainerState{
		"abcdef123456": {
			ID:         "abcdef123456",
			Name:       "api",
			LastExit:   42,
			IsAlerting: true,
		},
	}

	alert := oneDockerAlert(t, handleDockerEvent(containers, DockerEvent{
		ID:         "abcdef123456",
		Timestamp:  time.Unix(120, 0),
		Action:     "start",
		Attributes: map[string]string{"name": "api"},
	}, dockerEvalChannel(), "host-a", testDockerConfig()))
	if alert.IsFiring {
		t.Fatalf("expected resolved alert, got %+v", alert)
	}
	if alert.Title != "Container running" {
		t.Fatalf("title = %q", alert.Title)
	}
	if alert.Value != 0 {
		t.Fatalf("resolved value = %v, want 0", alert.Value)
	}
	if containers["abcdef123456"].IsAlerting {
		t.Fatal("container should no longer be alerting")
	}
}

func TestDockerRemoveResolvesActiveAlertAndDeletesState(t *testing.T) {
	containers := map[string]*ContainerState{
		"abcdef123456": {
			ID:         "abcdef123456",
			Name:       "api",
			LastExit:   42,
			IsAlerting: true,
		},
	}

	alert := oneDockerAlert(t, handleDockerEvent(containers, DockerEvent{
		ID:         "abcdef123456",
		Timestamp:  time.Unix(120, 0),
		Action:     "remove",
		Attributes: map[string]string{"name": "api"},
	}, dockerEvalChannel(), "host-a", testDockerConfig()))
	if alert.IsFiring || alert.Title != "Container removed" {
		t.Fatalf("expected container removed resolve, got %+v", alert)
	}
	if _, ok := containers["abcdef123456"]; ok {
		t.Fatal("removed container should be deleted from state")
	}
}

func TestDockerRemoveResolvesAllActiveAlerts(t *testing.T) {
	containers := map[string]*ContainerState{
		"abcdef123456": {
			ID:              "abcdef123456",
			Name:            "api",
			LastExit:        42,
			IsAlerting:      true,
			RestartAlerting: true,
			HealthAlerting:  true,
		},
	}

	alerts := handleDockerEvent(containers, DockerEvent{
		ID:         "abcdef123456",
		Timestamp:  time.Unix(120, 0),
		Action:     "remove",
		Attributes: map[string]string{"name": "api"},
	}, dockerEvalChannel(), "host-a", testDockerConfig())

	if len(alerts) != 3 {
		t.Fatalf("alerts = %d, want 3: %+v", len(alerts), alerts)
	}
	for _, alert := range alerts {
		if alert.IsFiring {
			t.Fatalf("expected resolve alert, got %+v", alert)
		}
	}
	if _, ok := containers["abcdef123456"]; ok {
		t.Fatal("removed container should be deleted from state")
	}
}

func TestDockerStatePersistsOnlyAlertingContainers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "docker-state.json")
	containers := map[string]*ContainerState{
		"alerting": {
			ID:              "alerting",
			Name:            "api",
			Image:           "alpine",
			LastExit:        42,
			IsAlerting:      true,
			RestartAlerting: true,
			HealthAlerting:  true,
		},
		"healthy": {
			ID:   "healthy",
			Name: "worker",
		},
	}

	if err := saveDockerState(path, containers); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("state file permissions: got %v", got)
	}
	loaded, err := loadDockerState(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(loaded) != 1 {
		t.Fatalf("loaded containers = %d, want 1", len(loaded))
	}
	container := loaded["alerting"]
	if container == nil || !container.IsAlerting || !container.RestartAlerting || !container.HealthAlerting || container.Name != "api" || container.LastExit != 42 {
		t.Fatalf("unexpected loaded state: %+v", loaded)
	}
}

func TestReconcileDockerAlerts(t *testing.T) {
	containers := map[string]*ContainerState{
		"running": {
			ID:         "running",
			Name:       "api",
			LastExit:   42,
			IsAlerting: true,
		},
		"stopped": {
			ID:         "stopped",
			Name:       "worker",
			LastExit:   1,
			IsAlerting: true,
		},
		"restarting": {
			ID:              "restarting",
			Name:            "jobs",
			RestartAlerting: true,
			StartTimes:      []time.Time{time.Unix(100, 0), time.Unix(101, 0), time.Unix(102, 0)},
		},
		"healthy": {
			ID:             "healthy",
			Name:           "web",
			HealthAlerting: true,
		},
		"missing": {
			ID:             "missing",
			Name:           "old",
			HealthAlerting: true,
		},
	}

	alerts := reconcileDockerAlerts(containers, map[string]dockerSnapshot{
		"running":    {Running: true},
		"stopped":    {},
		"restarting": {Running: true},
		"healthy":    {Running: true, Health: "healthy"},
	}, "host-a", testDockerConfig())

	if len(alerts) != 4 {
		t.Fatalf("alerts = %d, want 4: %+v", len(alerts), alerts)
	}
	for _, alert := range alerts {
		if alert.IsFiring {
			t.Fatalf("expected resolved alert, got %+v", alert)
		}
	}
	if containers["running"].IsAlerting {
		t.Fatal("running container should have resolved")
	}
	if !containers["stopped"].IsAlerting {
		t.Fatal("stopped container should still be alerting")
	}
	if containers["restarting"].RestartAlerting {
		t.Fatal("restart alert should resolve on startup reconciliation")
	}
	if containers["healthy"].HealthAlerting {
		t.Fatal("healthy container should resolve health alert")
	}
	if _, ok := containers["missing"]; ok {
		t.Fatal("missing container should be deleted from state")
	}
}

func TestDockerRestartLoopBuildsAndResolvesAlert(t *testing.T) {
	cfg := testDockerConfig()
	cfg.RestartThreshold = 3
	cfg.RestartWindow = Duration(time.Minute)
	containers := map[string]*ContainerState{}
	evalCh := dockerEvalChannel()

	for i := 0; i < 2; i++ {
		alerts := handleDockerEvent(containers, DockerEvent{
			ID:         "abcdef123456",
			Timestamp:  time.Unix(int64(100+i), 0),
			Action:     "start",
			Attributes: map[string]string{"name": "api"},
		}, evalCh, "host-a", cfg)
		if len(alerts) > 0 {
			t.Fatalf("unexpected alert before threshold: %+v", alerts)
		}
	}

	alert := oneDockerAlert(t, handleDockerEvent(containers, DockerEvent{
		ID:         "abcdef123456",
		Timestamp:  time.Unix(103, 0),
		Action:     "start",
		Attributes: map[string]string{"name": "api"},
	}, evalCh, "host-a", cfg))

	if !alert.IsFiring || alert.Title != "Container restart loop" {
		t.Fatalf("expected restart loop alert, got %+v", alert)
	}
	if !containers["abcdef123456"].RestartAlerting {
		t.Fatal("container should be restart alerting")
	}

	resolved := evaluateDockerRestartLoop(containers["abcdef123456"], "host-a", cfg, time.Unix(200, 0))
	if resolved == nil || resolved.IsFiring {
		t.Fatalf("expected restart loop resolve, got %+v", resolved)
	}
}

// Evaluating a still-active restart loop must not record a phantom start: the
// evaluation only re-checks the window, it is not itself a restart.
func TestDockerRestartLoopEvaluationDoesNotRecordPhantomStart(t *testing.T) {
	cfg := testDockerConfig()
	cfg.RestartThreshold = 3
	cfg.RestartWindow = Duration(time.Minute)
	c := &ContainerState{
		ID:              "abcdef123456",
		Name:            "api",
		RestartAlerting: true,
		StartTimes:      []time.Time{time.Unix(100, 0), time.Unix(101, 0), time.Unix(103, 0)},
	}

	// now=120 is still within the window of all three starts, so the loop has
	// not subsided: no resolve, and the count stays at the three real restarts.
	if alert := evaluateDockerRestartLoop(c, "host-a", cfg, time.Unix(120, 0)); alert != nil {
		t.Fatalf("expected no resolve while still looping, got %+v", alert)
	}
	if len(c.StartTimes) != 3 {
		t.Fatalf("evaluation recorded a phantom start: StartTimes = %d, want 3", len(c.StartTimes))
	}
}

func TestDockerHealthStatusAlertsAndResolves(t *testing.T) {
	containers := map[string]*ContainerState{}
	cfg := testDockerConfig()

	alert := oneDockerAlert(t, handleDockerEvent(containers, DockerEvent{
		ID:         "abcdef123456",
		Timestamp:  time.Unix(100, 0),
		Action:     "health_status: unhealthy",
		Attributes: map[string]string{"name": "api"},
	}, dockerEvalChannel(), "host-a", cfg))
	if !alert.IsFiring || alert.Title != "Container healthcheck unhealthy" {
		t.Fatalf("expected unhealthy alert, got %+v", alert)
	}

	alert = oneDockerAlert(t, handleDockerEvent(containers, DockerEvent{
		ID:         "abcdef123456",
		Timestamp:  time.Unix(110, 0),
		Action:     "health_status: healthy",
		Attributes: map[string]string{"name": "api"},
	}, dockerEvalChannel(), "host-a", cfg))
	if alert.IsFiring || alert.Title != "Container healthcheck healthy" {
		t.Fatalf("expected healthy resolve, got %+v", alert)
	}
}
