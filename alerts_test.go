package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

type captureNotifier struct{ alerts []Alert }

func (c *captureNotifier) Send(a Alert) error {
	c.alerts = append(c.alerts, a)
	return nil
}

func memMatcher(s MetricSample) bool { return s.Name == "memory.used_percent" }

func memSample(value float64) MetricSample {
	return MetricSample{Name: "memory.used_percent", Value: value, Unit: "percent", Collector: "memory"}
}

// newTestManager returns a manager whose clock is driven by the returned pointer,
// so tests can advance time without sleeping.
func newTestManager(rules []Rule, renotify time.Duration) (*AlertManager, *captureNotifier, *time.Time) {
	cap := &captureNotifier{}
	clock := time.Unix(0, 0)
	am := NewAlertManager(rules, renotify, "host", []Notifier{cap})
	am.now = func() time.Time { return clock }
	return am, cap, &clock
}

func newStaleTestManager(staleAfter time.Duration) (*AlertManager, *captureNotifier, *time.Time) {
	am, cap, clock := newTestManager([]Rule{memRule(2 * time.Minute)}, 10*time.Minute)
	am.StaleAfter = staleAfter
	am.Tracked = []TrackedMetric{{RuleID: "memory", Name: "memory.used_percent"}}
	return am, cap, clock
}

func memRule(forDur time.Duration) Rule {
	return Rule{
		ID:           "memory",
		Matcher:      memMatcher,
		Threshold:    80,
		ResolveBelow: 80,
		Message:      "High memory usage",
		Severity:     SeverityCritical,
		For:          forDur,
	}
}

func memRuleWithResolveBelow(forDur time.Duration, resolveBelow float64) Rule {
	rule := memRule(forDur)
	rule.ResolveBelow = resolveBelow
	return rule
}

func TestForDurationPendingThenFires(t *testing.T) {
	am, cap, clock := newTestManager([]Rule{memRule(2 * time.Minute)}, time.Hour)

	am.Evaluate(memSample(90)) // t=0: pending, no alert
	if len(cap.alerts) != 0 {
		t.Fatalf("expected no alert while pending, got %d", len(cap.alerts))
	}

	*clock = clock.Add(90 * time.Second) // still under 2m
	am.Evaluate(memSample(90))
	if len(cap.alerts) != 0 {
		t.Fatalf("expected no alert at 90s, got %d", len(cap.alerts))
	}

	*clock = clock.Add(40 * time.Second) // 130s >= 120s
	am.Evaluate(memSample(90))
	if len(cap.alerts) != 1 || !cap.alerts[0].IsFiring {
		t.Fatalf("expected one firing alert, got %+v", cap.alerts)
	}

	am.Evaluate(memSample(90)) // already firing, no duplicate
	if len(cap.alerts) != 1 {
		t.Fatalf("expected no duplicate, got %d", len(cap.alerts))
	}
}

func TestMemoryRuleFiresAfterConfiguredFiveMinutes(t *testing.T) {
	am, cap, clock := newTestManager([]Rule{memRule(5 * time.Minute)}, time.Hour)

	am.Evaluate(memSample(80)) // equal to threshold is not a breach
	if len(cap.alerts) != 0 {
		t.Fatalf("expected no alert at threshold, got %d", len(cap.alerts))
	}

	am.Evaluate(memSample(81)) // t=0: pending starts
	*clock = clock.Add(4*time.Minute + 59*time.Second)
	am.Evaluate(memSample(81))
	if len(cap.alerts) != 0 {
		t.Fatalf("expected no alert before five minutes, got %d", len(cap.alerts))
	}

	*clock = clock.Add(time.Second)
	am.Evaluate(memSample(81))
	if len(cap.alerts) != 1 || !cap.alerts[0].IsFiring {
		t.Fatalf("expected firing alert after five minutes, got %+v", cap.alerts)
	}
}

func TestDipResetsPendingClock(t *testing.T) {
	am, cap, clock := newTestManager([]Rule{memRule(2 * time.Minute)}, time.Hour)

	am.Evaluate(memSample(90)) // t=0: pending
	*clock = clock.Add(90 * time.Second)
	am.Evaluate(memSample(50)) // recovers before firing -> resets, no alert
	if len(cap.alerts) != 0 {
		t.Fatalf("expected no alert, got %d", len(cap.alerts))
	}

	am.Evaluate(memSample(90)) // pending restarts at t=90s
	*clock = clock.Add(90 * time.Second)
	am.Evaluate(memSample(90)) // only 90s into the new streak
	if len(cap.alerts) != 0 {
		t.Fatalf("expected clock to have reset, got %d alerts", len(cap.alerts))
	}

	*clock = clock.Add(40 * time.Second) // 130s into new streak
	am.Evaluate(memSample(90))
	if len(cap.alerts) != 1 {
		t.Fatalf("expected one firing alert after the new streak, got %d", len(cap.alerts))
	}
}

func TestForZeroFiresImmediately(t *testing.T) {
	am, cap, _ := newTestManager([]Rule{memRule(0)}, time.Hour)

	am.Evaluate(memSample(90))
	if len(cap.alerts) != 1 || !cap.alerts[0].IsFiring {
		t.Fatalf("expected immediate firing alert, got %+v", cap.alerts)
	}
}

func TestResolveAfterFiring(t *testing.T) {
	am, cap, _ := newTestManager([]Rule{memRule(0)}, time.Hour)

	am.Evaluate(memSample(90)) // fires
	am.Evaluate(memSample(50)) // resolves
	if len(cap.alerts) != 2 {
		t.Fatalf("expected fire + resolve, got %d", len(cap.alerts))
	}
	if cap.alerts[1].IsFiring {
		t.Fatalf("expected second alert to be a resolve")
	}
}

func TestResolveBelowPreventsFlappingNearThreshold(t *testing.T) {
	am, cap, _ := newTestManager([]Rule{memRuleWithResolveBelow(0, 75)}, time.Hour)

	am.Evaluate(memSample(81))   // fires
	am.Evaluate(memSample(79.9)) // below fire threshold, above resolve threshold: stays firing
	am.Evaluate(memSample(80.1)) // crosses fire threshold again, but was already firing
	if len(cap.alerts) != 1 || !cap.alerts[0].IsFiring {
		t.Fatalf("expected alert to stay firing without flapping, got %+v", cap.alerts)
	}

	am.Evaluate(memSample(75)) // finally resolves
	if len(cap.alerts) != 2 || cap.alerts[1].IsFiring {
		t.Fatalf("expected resolve at resolve threshold, got %+v", cap.alerts)
	}
}

func TestResolveBelowZeroIsValid(t *testing.T) {
	am, cap, _ := newTestManager([]Rule{memRuleWithResolveBelow(0, 0)}, time.Hour)

	am.Evaluate(memSample(81))
	am.Evaluate(memSample(0.1))
	if len(cap.alerts) != 1 {
		t.Fatalf("expected alert to stay firing above zero resolve threshold, got %+v", cap.alerts)
	}

	am.Evaluate(memSample(0))
	if len(cap.alerts) != 2 || cap.alerts[1].IsFiring {
		t.Fatalf("expected alert to resolve at zero, got %+v", cap.alerts)
	}
}

func TestRecoveryBandDoesNotRenotify(t *testing.T) {
	am, cap, clock := newTestManager([]Rule{memRuleWithResolveBelow(0, 75)}, 10*time.Minute)

	am.Evaluate(memSample(81)) // fires at t=0
	*clock = clock.Add(10 * time.Minute)
	am.Evaluate(memSample(79)) // still firing, but below fire threshold
	if len(cap.alerts) != 1 {
		t.Fatalf("expected no renotify while in recovery band, got %+v", cap.alerts)
	}

	am.Evaluate(memSample(81)) // now above fire threshold again, renotify budget has elapsed
	if len(cap.alerts) != 2 || !cap.alerts[1].IsFiring {
		t.Fatalf("expected renotify when above fire threshold again, got %+v", cap.alerts)
	}
}

func TestRenotifyAfter(t *testing.T) {
	am, cap, clock := newTestManager([]Rule{memRule(0)}, 10*time.Minute)

	am.Evaluate(memSample(90)) // fires at t=0
	*clock = clock.Add(5 * time.Minute)
	am.Evaluate(memSample(90)) // too soon to renotify
	if len(cap.alerts) != 1 {
		t.Fatalf("expected no renotify at 5m, got %d", len(cap.alerts))
	}

	*clock = clock.Add(5 * time.Minute) // t=10m
	am.Evaluate(memSample(90))
	if len(cap.alerts) != 2 || !cap.alerts[1].IsFiring {
		t.Fatalf("expected renotify at 10m, got %+v", cap.alerts)
	}
}

// TestTwoRulesSameMetricIndependent guards the #20 fix: two rules matching the
// same metric must keep separate state instead of sharing one entry.
func TestTwoRulesSameMetricIndependent(t *testing.T) {
	warn := Rule{ID: "mem-warn", Matcher: memMatcher, Threshold: 80, ResolveBelow: 80, Message: "warn", Severity: SeverityWarning}
	crit := Rule{ID: "mem-crit", Matcher: memMatcher, Threshold: 90, ResolveBelow: 90, Message: "crit", Severity: SeverityCritical}
	am, cap, _ := newTestManager([]Rule{warn, crit}, time.Hour)

	am.Evaluate(memSample(85)) // over warn(80), under crit(90)
	if len(cap.alerts) != 1 || cap.alerts[0].Severity != SeverityWarning {
		t.Fatalf("expected only the warning to fire, got %+v", cap.alerts)
	}

	am.Evaluate(memSample(95)) // now over both; warn already firing, crit fires
	if len(cap.alerts) != 2 || cap.alerts[1].Severity != SeverityCritical {
		t.Fatalf("expected the critical to fire independently, got %+v", cap.alerts)
	}
}

func TestExpectedMetricStaleWhenNeverSeen(t *testing.T) {
	am, cap, clock := newStaleTestManager(time.Minute)

	am.CheckStale() // starts the missing clock
	*clock = clock.Add(59 * time.Second)
	am.CheckStale()
	if len(cap.alerts) != 0 {
		t.Fatalf("expected no stale alert before timeout, got %+v", cap.alerts)
	}

	*clock = clock.Add(time.Second)
	am.CheckStale()
	if len(cap.alerts) != 1 || !cap.alerts[0].IsFiring {
		t.Fatalf("expected stale firing alert, got %+v", cap.alerts)
	}
	if cap.alerts[0].Title != "Metric not reporting" || cap.alerts[0].Metric != "memory.used_percent" {
		t.Fatalf("unexpected stale alert: %+v", cap.alerts[0])
	}
}

func TestStaleMetricResolvesWhenSampleReturns(t *testing.T) {
	am, cap, clock := newStaleTestManager(time.Minute)

	am.CheckStale()
	*clock = clock.Add(time.Minute)
	am.CheckStale()
	am.Evaluate(memSample(50))

	if len(cap.alerts) != 2 {
		t.Fatalf("expected stale firing + resolved, got %+v", cap.alerts)
	}
	if cap.alerts[1].IsFiring || cap.alerts[1].Title != "Metric reporting restored" {
		t.Fatalf("expected stale resolved alert, got %+v", cap.alerts[1])
	}
}

func TestSeenMetricBecomesStale(t *testing.T) {
	am, cap, clock := newStaleTestManager(time.Minute)

	am.Evaluate(memSample(50))
	*clock = clock.Add(time.Minute)
	am.CheckStale()

	if len(cap.alerts) != 1 || !cap.alerts[0].IsFiring {
		t.Fatalf("expected seen metric to become stale, got %+v", cap.alerts)
	}
}

func TestStaleMetricRenotifiesAfterBudget(t *testing.T) {
	am, cap, clock := newStaleTestManager(time.Minute)

	am.Evaluate(memSample(50))
	*clock = clock.Add(time.Minute)
	am.CheckStale()
	*clock = clock.Add(9 * time.Minute)
	am.CheckStale()
	if len(cap.alerts) != 1 {
		t.Fatalf("expected no stale renotify before budget, got %+v", cap.alerts)
	}

	*clock = clock.Add(time.Minute)
	am.CheckStale()
	if len(cap.alerts) != 2 || !cap.alerts[1].IsFiring {
		t.Fatalf("expected stale renotify after budget, got %+v", cap.alerts)
	}
}

func TestAlertStatePersistsFiringAcrossRestart(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")

	am, cap, _ := newTestManager([]Rule{memRule(0)}, time.Hour)
	am.StateFile = stateFile
	am.Evaluate(memSample(90))
	if len(cap.alerts) != 1 || !cap.alerts[0].IsFiring {
		t.Fatalf("expected initial firing alert, got %+v", cap.alerts)
	}

	restarted, restartedCap, _ := newTestManager([]Rule{memRule(0)}, time.Hour)
	restarted.StateFile = stateFile
	if err := restarted.LoadState(stateFile); err != nil {
		t.Fatal(err)
	}

	restarted.Evaluate(memSample(90))
	if len(restartedCap.alerts) != 0 {
		t.Fatalf("expected no duplicate firing alert after restart, got %+v", restartedCap.alerts)
	}

	restarted.Evaluate(memSample(50))
	if len(restartedCap.alerts) != 1 || restartedCap.alerts[0].IsFiring {
		t.Fatalf("expected recovered alert after persisted firing state, got %+v", restartedCap.alerts)
	}
}

func TestLoadStateMissingFileIsOK(t *testing.T) {
	am, _, _ := newTestManager([]Rule{memRule(0)}, time.Hour)
	if err := am.LoadState(filepath.Join(t.TempDir(), "missing.json")); err != nil {
		t.Fatal(err)
	}
}

func TestSaveStateCreatesPrivateFile(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "nested", "state.json")
	am, _, _ := newTestManager([]Rule{memRule(0)}, time.Hour)
	am.StateFile = stateFile

	am.Evaluate(memSample(90))

	info, err := os.Stat(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("state file permissions: got %v", got)
	}
}
