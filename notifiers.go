package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Notifier interface {
	Send(alert Alert) error
}

var errNotifierQueueFull = errors.New("notifier queue is full")

type asyncNotifier struct {
	queue     chan Alert
	notifiers []Notifier
}

func newAsyncNotifier(notifiers []Notifier, capacity int) *asyncNotifier {
	if capacity < 1 {
		capacity = 1
	}
	return &asyncNotifier{
		queue:     make(chan Alert, capacity),
		notifiers: notifiers,
	}
}

func (n *asyncNotifier) Send(alert Alert) error {
	select {
	case n.queue <- alert:
		return nil
	default:
		return errNotifierQueueFull
	}
}

func (n *asyncNotifier) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case alert := <-n.queue:
			n.send(alert)
		}
	}
}

func (n *asyncNotifier) send(alert Alert) {
	for _, notifier := range n.notifiers {
		if err := notifier.Send(alert); err != nil {
			log.Printf("error sending alert: %v", err)
		}
	}
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

// safeURL reduces a URL to scheme://host so tokens embedded in the path or query
// (e.g. a Telegram bot token, a Google Chat key/token) never reach the logs.
func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<redacted url>"
	}
	return u.Scheme + "://" + u.Host
}

// unwrapURL strips the address from a *url.Error so the underlying cause can be
// logged without the secret-bearing URL it carries.
func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// alertVisual maps an alert's state and severity to the colour/label each
// channel renders: green when resolved, red (critical) or amber (warning).
type alertVisual struct {
	label        string
	emoji        string
	hex          string
	discordColor int
	slackColor   string
}

func visualFor(alert Alert) alertVisual {
	switch {
	case !alert.IsFiring:
		return alertVisual{label: "RESOLVED", emoji: "✅", hex: "#2ECC71", discordColor: 3066993, slackColor: "good"}
	case alert.Severity == SeverityWarning:
		return alertVisual{label: "WARNING", emoji: "🟡", hex: "#F1C40F", discordColor: 15844367, slackColor: "warning"}
	default:
		return alertVisual{label: "CRITICAL", emoji: "🔴", hex: "#E74C3C", discordColor: 15158332, slackColor: "danger"}
	}
}

func formatAlert(alert Alert) string {
	v := visualFor(alert)
	return fmt.Sprintf("%s %s — %s\nHost: %s\nMetric: %s\nValue: %s (threshold %s)",
		v.emoji, v.label, alert.Title, alert.Hostname, alert.Metric,
		formatValue(alert.Value, alert.Unit), formatValue(alert.Threshold, alert.Unit))
}

func formatValue(value float64, unit string) string {
	if unit == "percent" {
		return fmt.Sprintf("%.2f%%", value)
	}
	return fmt.Sprintf("%.2f %s", value, unit)
}

const maxSendAttempts = 3

// postJSON POSTs payload as JSON, retrying transient failures (network, 5xx,
// 429) with backoff; other 4xx are returned immediately.
func postJSON(url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	var lastErr error
	for attempt := 0; attempt < maxSendAttempts; attempt++ {
		wait := time.Duration(attempt+1) * time.Second

		resp, err := httpClient.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", safeURL(url), unwrapURL(err))
		} else {
			status := resp.StatusCode
			retryAfter := resp.Header.Get("Retry-After")
			_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection can be reused
			resp.Body.Close()

			if status >= 200 && status < 300 {
				return nil
			}
			lastErr = fmt.Errorf("%s returned status %d", safeURL(url), status)

			// 4xx other than 429 won't be fixed by retrying
			if status < 500 && status != http.StatusTooManyRequests {
				return lastErr
			}
			if status == http.StatusTooManyRequests {
				if secs, e := strconv.Atoi(retryAfter); e == nil && secs > 0 && secs <= 60 {
					wait = time.Duration(secs) * time.Second
				}
			}
		}

		if attempt < maxSendAttempts-1 {
			time.Sleep(wait)
		}
	}
	return lastErr
}

type ConsoleNotifier struct{}

func (c *ConsoleNotifier) Send(alert Alert) error {
	fmt.Println(formatAlert(alert))
	return nil
}

type GoogleChatNotifier struct {
	WebhookURL string
}

func (g *GoogleChatNotifier) Send(alert Alert) error {
	return postJSON(g.WebhookURL, map[string]string{"text": formatAlert(alert)})
}

type DiscordNotifier struct {
	WebhookURL string
}

func (d *DiscordNotifier) Send(alert Alert) error {
	v := visualFor(alert)
	return postJSON(d.WebhookURL, map[string]any{
		"embeds": []map[string]any{{
			"description": formatAlert(alert),
			"color":       v.discordColor,
		}},
	})
}

type SlackNotifier struct {
	WebhookURL string
}

func (s *SlackNotifier) Send(alert Alert) error {
	v := visualFor(alert)
	text := formatAlert(alert)
	return postJSON(s.WebhookURL, map[string]any{
		"attachments": []map[string]any{{
			"color":    v.slackColor,
			"text":     text,
			"fallback": text,
		}},
	})
}

type TeamsNotifier struct {
	WebhookURL string
}

func (t *TeamsNotifier) Send(alert Alert) error {
	v := visualFor(alert)
	return postJSON(t.WebhookURL, map[string]any{
		"type": "message",
		"attachments": []map[string]any{{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"content": map[string]any{
				"type":    "AdaptiveCard",
				"version": "1.4",
				"body": []map[string]any{
					{
						"type":   "TextBlock",
						"text":   v.label + " - " + alert.Title,
						"weight": "Bolder",
						"color":  teamsColor(alert),
						"wrap":   true,
					},
					{
						"type": "FactSet",
						"facts": []map[string]string{
							{"title": "Host", "value": alert.Hostname},
							{"title": "Metric", "value": alert.Metric},
							{"title": "Value", "value": formatValue(alert.Value, alert.Unit)},
							{"title": "Threshold", "value": formatValue(alert.Threshold, alert.Unit)},
						},
					},
				},
			},
		}},
	})
}

func teamsColor(alert Alert) string {
	if !alert.IsFiring {
		return "Good"
	}
	if alert.Severity == SeverityCritical {
		return "Attention"
	}
	return "Warning"
}

type TelegramNotifier struct {
	BotToken string
	ChatID   string
}

func (t *TelegramNotifier) Send(alert Alert) error {
	url := "https://api.telegram.org/bot" + t.BotToken + "/sendMessage"
	return postJSON(url, map[string]any{
		"chat_id": t.ChatID,
		"text":    formatAlert(alert),
	})
}

type PagerDutyNotifier struct {
	IntegrationKey string
}

func (p *PagerDutyNotifier) Send(alert Alert) error {
	action := "trigger"
	if !alert.IsFiring {
		action = "resolve"
	}

	return postJSON("https://events.pagerduty.com/v2/enqueue", map[string]any{
		"routing_key":  p.IntegrationKey,
		"event_action": action,
		"dedup_key":    alert.Hostname + ":" + alert.Metric,
		"payload": map[string]any{
			"summary":   alert.Title + " on " + alert.Hostname,
			"source":    alert.Hostname,
			"severity":  pagerDutySeverity(alert),
			"component": alert.Metric,
			"group":     "lookout",
			"class":     alert.Unit,
			"custom_details": map[string]any{
				"metric":    alert.Metric,
				"value":     alert.Value,
				"threshold": alert.Threshold,
				"unit":      alert.Unit,
				"status":    alertStatus(alert),
			},
		},
	})
}

func pagerDutySeverity(alert Alert) string {
	if alert.Severity == SeverityCritical {
		return "critical"
	}
	return "warning"
}

func alertStatus(alert Alert) string {
	if alert.IsFiring {
		return "firing"
	}
	return "resolved"
}

type GenericWebhookNotifier struct {
	WebhookURL string
}

// GenericWebhookNotifier POSTs all alert fields as structured JSON for custom integrations.
func (g *GenericWebhookNotifier) Send(alert Alert) error {
	v := visualFor(alert)
	return postJSON(g.WebhookURL, map[string]any{
		"status":    alertStatus(alert),
		"severity":  string(alert.Severity),
		"title":     alert.Title,
		"metric":    alert.Metric,
		"value":     alert.Value,
		"threshold": alert.Threshold,
		"unit":      alert.Unit,
		"hostname":  alert.Hostname,
		"color":     v.hex,
		"text":      formatAlert(alert),
	})
}
