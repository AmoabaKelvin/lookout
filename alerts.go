package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
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
	StateFile     string
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

type storedAlertState struct {
	RuleID            string    `json:"rule_id"`
	MetricName        string    `json:"metric_name"`
	IsFiring          bool      `json:"is_firing"`
	PendingSince      time.Time `json:"pending_since,omitempty"`
	LastNotified      time.Time `json:"last_notified,omitempty"`
	LastSeen          time.Time `json:"last_seen,omitempty"`
	MissingSince      time.Time `json:"missing_since,omitempty"`
	IsStale           bool      `json:"is_stale"`
	StaleLastNotified time.Time `json:"stale_last_notified,omitempty"`
}

func stateKey(ruleID, metricName string) string {
	return ruleID + "|" + metricName
}

func (am *AlertManager) LoadState(path string) error {
	if path == "" {
		return nil
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	var stored []storedAlertState
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}

	for _, s := range stored {
		if s.RuleID == "" || s.MetricName == "" {
			continue
		}
		am.StateManager[stateKey(s.RuleID, s.MetricName)] = &alertState{
			ruleID:            s.RuleID,
			metricName:        s.MetricName,
			isFiring:          s.IsFiring,
			pendingSince:      s.PendingSince,
			lastNotified:      s.LastNotified,
			lastSeen:          s.LastSeen,
			missingSince:      s.MissingSince,
			isStale:           s.IsStale,
			staleLastNotified: s.StaleLastNotified,
		}
	}
	return nil
}

func (am *AlertManager) saveState() {
	if am.StateFile == "" {
		return
	}
	if err := am.writeState(am.StateFile); err != nil {
		log.Printf("error saving alert state: %v", err)
	}
}

func (am *AlertManager) writeState(path string) error {
	stored := make([]storedAlertState, 0, len(am.StateManager))
	for _, s := range am.StateManager {
		stored = append(stored, storedAlertState{
			RuleID:            s.ruleID,
			MetricName:        s.metricName,
			IsFiring:          s.isFiring,
			PendingSince:      s.pendingSince,
			LastNotified:      s.lastNotified,
			LastSeen:          s.lastSeen,
			MissingSince:      s.missingSince,
			IsStale:           s.isStale,
			StaleLastNotified: s.staleLastNotified,
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

	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
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
	key := stateKey(rule.ID, metricName)
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
		stateChanged := false
		if state.isStale {
			age := state.missingFor(now)
			am.dispatch(am.buildStaleAlert(sample.Name, false, age))
			state.isStale = false
			state.staleLastNotified = time.Time{}
			stateChanged = true
		}
		state.lastSeen = now
		state.missingSince = time.Time{}
		if stateChanged {
			am.saveState()
		}

		if state.isFiring {
			isResolved := sample.Value <= rule.ResolveBelow
			isBreaching := sample.Value > rule.Threshold

			if isResolved {
				state.pendingSince = time.Time{}
				am.dispatch(am.buildAlert(rule, sample, false))
				state.isFiring = false
				am.saveState()
				continue
			}
			if !isBreaching {
				continue
			}

			// still above the firing threshold: nudge again only after RenotifyAfter
			if now.Sub(state.lastNotified) >= am.RenotifyAfter {
				am.dispatch(am.buildAlert(rule, sample, true))
				state.lastNotified = now
				am.saveState()
			}
			continue
		}

		if sample.Value <= rule.Threshold {
			if !state.pendingSince.IsZero() {
				state.pendingSince = time.Time{}
				am.saveState()
			}
			continue
		}

		// breached: start the clock if this is the first sample over the line
		if state.pendingSince.IsZero() {
			state.pendingSince = now
			am.saveState()
		}

		// pending: fire once the breach has persisted for at least rule.For
		if now.Sub(state.pendingSince) >= rule.For {
			am.dispatch(am.buildAlert(rule, sample, true))
			state.isFiring = true
			state.lastNotified = now
			am.saveState()
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
				am.saveState()
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
				am.saveState()
			}
			continue
		}

		am.dispatch(am.buildStaleAlert(state.metricName, true, missingFor))
		state.isStale = true
		state.staleLastNotified = now
		am.saveState()
	}
}
