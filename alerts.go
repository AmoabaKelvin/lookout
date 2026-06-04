// most of the code here translates for the numeric based events
// and Docker has not yet been considered, this is something we
// would have to extend in the future.

package main

import (
	"log"
	"time"
)

// Rule represents a condition to trigger alerts on metric samples
// in our discussions this will be the things that the user configures
// because we will create matching rules for them all. assuming the user
// configures thresholds for memory usage or the disk usage, we now have to represent
// it internally using this rule struct.
type Rule struct {
	Matcher   func(MetricSample) bool // this tells us whether this rule should fire for any metric
	Threshold float64
	Message   string
}

// Alert represents a notification about a metric breach or recovery
type Alert struct {
	IsFiring  bool
	Title     string
	Value     float64
	Threshold float64
	Hostname  string
	Metric    string
	Unit      string
}

// alertState represents the current state of an alert (firing or not)
// it helps track whether we have already seen an alert and whether it is currently
// firing, which means it has crossed its threshold and triggering notifications (albeit debounced)
// so once say an alert now stops going over the threshold, we set the isFiring flag to false.
type alertState struct {
	isFiring     bool
	lastNotified time.Time
}

// AlertManager manages alert evaluation and notification dispatch
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
				// check if we are already in the firing state for this issue
				// then if we are, check whether or not if we have passed the threshold
				// for when we should re-alert, then if we have, nudge again.
				// if we have not sent before, fire the notification, record
				// the time, and then set it to firing so we know to track
				if !state.isFiring || time.Since(state.lastNotified) >= am.RenotifyAfter {
					// Fire alert notification
					// we are sending to all enabled notification channels
					am.dispatch(Alert{
						IsFiring:  true,
						Title:     rule.Message,
						Value:     sample.Value,
						Threshold: rule.Threshold,
						Hostname:  am.Hostname,
						Metric:    sample.Name,
						Unit:      sample.Unit,
					})
					state.isFiring = true
					state.lastNotified = time.Now()
				}
			} else {
				// check if we were previously firing and need to resolve
				// if we were in a firing state, send recovery notification
				if state.isFiring {
					am.dispatch(Alert{
						IsFiring:  false,
						Title:     rule.Message,
						Value:     sample.Value,
						Threshold: rule.Threshold,
						Hostname:  am.Hostname,
						Metric:    sample.Name,
						Unit:      sample.Unit,
					})
					state.isFiring = false
				}
			}
		}
	}
}
