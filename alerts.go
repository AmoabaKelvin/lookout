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
	Matcher   func(MetricSample) bool
	Threshold float64
	Message   string
	Severity  Severity
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

type alertState struct {
	isFiring     bool
	lastNotified time.Time
}

type AlertManager struct {
	Rules         []Rule
	StateManager  map[string]*alertState
	RenotifyAfter time.Duration
	Hostname      string
	Notifiers     []Notifier
}

func NewAlertManager(rules []Rule, renotifyAfter time.Duration, hostname string, notifiers []Notifier) *AlertManager {
	return &AlertManager{
		Rules:         rules,
		StateManager:  make(map[string]*alertState),
		RenotifyAfter: renotifyAfter,
		Hostname:      hostname,
		Notifiers:     notifiers,
	}
}

func (am *AlertManager) dispatch(alert Alert) {
	for _, n := range am.Notifiers {
		if err := n.Send(alert); err != nil {
			log.Printf("error sending alert: %v", err)
		}
	}
}

func (am *AlertManager) Evaluate(sample MetricSample) {
	for _, rule := range am.Rules {
		if rule.Matcher(sample) {
			breached := sample.Value > rule.Threshold

			state := am.StateManager[sample.Name]
			if state == nil {
				state = &alertState{}
				am.StateManager[sample.Name] = state
			}

			if breached {
				// fire on first breach, then again only after RenotifyAfter
				if !state.isFiring || time.Since(state.lastNotified) >= am.RenotifyAfter {
					am.dispatch(Alert{
						IsFiring:  true,
						Title:     rule.Message,
						Value:     sample.Value,
						Threshold: rule.Threshold,
						Hostname:  am.Hostname,
						Metric:    sample.Name,
						Unit:      sample.Unit,
						Severity:  rule.Severity,
					})
					state.isFiring = true
					state.lastNotified = time.Now()
				}
			} else {
				if state.isFiring {
					am.dispatch(Alert{
						IsFiring:  false,
						Title:     rule.Message,
						Value:     sample.Value,
						Threshold: rule.Threshold,
						Hostname:  am.Hostname,
						Metric:    sample.Name,
						Unit:      sample.Unit,
						Severity:  rule.Severity,
					})
					state.isFiring = false
				}
			}
		}
	}
}
