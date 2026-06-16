package main

import (
	"log"
	"time"
)

type Severity string

const (
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

type Rule struct {
	ID           string
	Matcher      func(MetricSample) bool
	Threshold    float64
	ResolveBelow float64
	Message      string
	Severity     Severity
	For          time.Duration // how long a breach must persist before firing
}

type TrackedMetric struct {
	RuleID string
	Name   string
}

type Alert struct {
	IsFiring  bool
	Title     string
	Value     float64
	Threshold float64
	Hostname  string
	Metric    string
	Unit      string
	Severity  Severity
}

// alertState tracks one rule+metric pair through threshold and staleness alerts.
type alertState struct {
	ruleID            string
	metricName        string
	isFiring          bool
	pendingSince      time.Time // when the current breach streak began; zero when not breached
	lastNotified      time.Time
	lastSeen          time.Time
	missingSince      time.Time
	isStale           bool
	staleLastNotified time.Time
}

type AlertManager struct {
	Rules         []Rule
	StateManager  map[string]*alertState
	RenotifyAfter time.Duration
	StaleAfter    time.Duration
	Tracked       []TrackedMetric
	Hostname      string
	Notifiers     []Notifier
	now           func() time.Time // injectable for tests
}

func NewAlertManager(rules []Rule, renotifyAfter time.Duration, hostname string, notifiers []Notifier) *AlertManager {
	return &AlertManager{
		Rules:         rules,
		StateManager:  make(map[string]*alertState),
		RenotifyAfter: renotifyAfter,
		Hostname:      hostname,
		Notifiers:     notifiers,
		now:           time.Now,
	}
}

func (am *AlertManager) dispatch(alert Alert) {
	for _, n := range am.Notifiers {
		if err := n.Send(alert); err != nil {
			log.Printf("error sending alert: %v", err)
		}
	}
}

func (am *AlertManager) buildAlert(rule Rule, sample MetricSample, firing bool) Alert {
	return Alert{
		IsFiring:  firing,
		Title:     rule.Message,
		Value:     sample.Value,
		Threshold: rule.Threshold,
		Hostname:  am.Hostname,
		Metric:    sample.Name,
		Unit:      sample.Unit,
		Severity:  rule.Severity,
	}
}

func (am *AlertManager) buildStaleAlert(metricName string, firing bool, age time.Duration) Alert {
	title := "Metric not reporting"
	if !firing {
		title = "Metric reporting restored"
	}
	return Alert{
		IsFiring:  firing,
		Title:     title,
		Value:     age.Seconds(),
		Threshold: am.StaleAfter.Seconds(),
		Hostname:  am.Hostname,
		Metric:    metricName,
		Unit:      "seconds",
		Severity:  SeverityWarning,
	}
}

func (s *alertState) missingFor(now time.Time) time.Duration {
	if !s.lastSeen.IsZero() {
		return now.Sub(s.lastSeen)
	}
	if !s.missingSince.IsZero() {
		return now.Sub(s.missingSince)
	}
	return 0
}

func (am *AlertManager) stateFor(rule Rule, metricName string) *alertState {
	key := rule.ID + "|" + metricName
	state := am.StateManager[key]
	if state == nil {
		state = &alertState{ruleID: rule.ID, metricName: metricName}
		am.StateManager[key] = state
	}
	return state
}

func (am *AlertManager) ruleByID(id string) (Rule, bool) {
	for _, rule := range am.Rules {
		if rule.ID == id {
			return rule, true
		}
	}
	return Rule{}, false
}

func (am *AlertManager) Evaluate(sample MetricSample) {
	now := am.now()

	for _, rule := range am.Rules {
		if !rule.Matcher(sample) {
			continue
		}

		state := am.stateFor(rule, sample.Name)
		if state.isStale {
			age := state.missingFor(now)
			am.dispatch(am.buildStaleAlert(sample.Name, false, age))
			state.isStale = false
			state.staleLastNotified = time.Time{}
		}
		state.lastSeen = now
		state.missingSince = time.Time{}

		if state.isFiring {
			isResolved := sample.Value <= rule.ResolveBelow
			isBreaching := sample.Value > rule.Threshold

			if isResolved {
				state.pendingSince = time.Time{}
				am.dispatch(am.buildAlert(rule, sample, false))
				state.isFiring = false
				continue
			}
			if !isBreaching {
				continue
			}

			// still above the firing threshold: nudge again only after RenotifyAfter
			if now.Sub(state.lastNotified) >= am.RenotifyAfter {
				am.dispatch(am.buildAlert(rule, sample, true))
				state.lastNotified = now
			}
			continue
		}

		if sample.Value <= rule.Threshold {
			state.pendingSince = time.Time{}
			continue
		}

		// breached: start the clock if this is the first sample over the line
		if state.pendingSince.IsZero() {
			state.pendingSince = now
		}

		// pending: fire once the breach has persisted for at least rule.For
		if now.Sub(state.pendingSince) >= rule.For {
			am.dispatch(am.buildAlert(rule, sample, true))
			state.isFiring = true
			state.lastNotified = now
		}
	}
}

func (am *AlertManager) CheckStale() {
	if am.StaleAfter <= 0 {
		return
	}

	now := am.now()
	for _, tracked := range am.Tracked {
		if rule, ok := am.ruleByID(tracked.RuleID); ok {
			am.stateFor(rule, tracked.Name)
		}
	}

	for _, state := range am.StateManager {
		missingFor := state.missingFor(now)
		if state.lastSeen.IsZero() {
			if state.missingSince.IsZero() {
				state.missingSince = now
				continue
			}
		}

		if missingFor < am.StaleAfter {
			continue
		}

		state.pendingSince = time.Time{}
		if state.isStale {
			if now.Sub(state.staleLastNotified) >= am.RenotifyAfter {
				am.dispatch(am.buildStaleAlert(state.metricName, true, missingFor))
				state.staleLastNotified = now
			}
			continue
		}

		am.dispatch(am.buildStaleAlert(state.metricName, true, missingFor))
		state.isStale = true
		state.staleLastNotified = now
	}
}
