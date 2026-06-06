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
	ID        string
	Matcher   func(MetricSample) bool
	Threshold float64
	Message   string
	Severity  Severity
	For       time.Duration // how long a breach must persist before firing
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

// alertState tracks one rule+metric pair through inactive -> pending -> firing.
type alertState struct {
	isFiring     bool
	pendingSince time.Time // when the current breach streak began; zero when not breached
	lastNotified time.Time
}

type AlertManager struct {
	Rules         []Rule
	StateManager  map[string]*alertState
	RenotifyAfter time.Duration
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

func (am *AlertManager) Evaluate(sample MetricSample) {
	now := am.now()

	for _, rule := range am.Rules {
		if !rule.Matcher(sample) {
			continue
		}

		key := rule.ID + "|" + sample.Name
		state := am.StateManager[key]
		if state == nil {
			state = &alertState{}
			am.StateManager[key] = state
		}

		if sample.Value <= rule.Threshold {
			state.pendingSince = time.Time{}
			if state.isFiring {
				am.dispatch(am.buildAlert(rule, sample, false))
				state.isFiring = false
			}
			continue
		}

		// breached: start the clock if this is the first sample over the line
		if state.pendingSince.IsZero() {
			state.pendingSince = now
		}

		if state.isFiring {
			// already firing: nudge again only after RenotifyAfter
			if now.Sub(state.lastNotified) >= am.RenotifyAfter {
				am.dispatch(am.buildAlert(rule, sample, true))
				state.lastNotified = now
			}
			continue
		}

		// pending: fire once the breach has persisted for at least rule.For
		if now.Sub(state.pendingSince) >= rule.For {
			am.dispatch(am.buildAlert(rule, sample, true))
			state.isFiring = true
			state.lastNotified = now
		}
	}
}
