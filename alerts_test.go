package main

import (
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

func memRule(forDur time.Duration) Rule {
	return Rule{
		ID:        "memory",
		Matcher:   memMatcher,
		Threshold: 80,
		Message:   "High memory usage",
		Severity:  SeverityCritical,
		For:       forDur,
	}
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
	warn := Rule{ID: "mem-warn", Matcher: memMatcher, Threshold: 80, Message: "warn", Severity: SeverityWarning}
	crit := Rule{ID: "mem-crit", Matcher: memMatcher, Threshold: 90, Message: "crit", Severity: SeverityCritical}
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
