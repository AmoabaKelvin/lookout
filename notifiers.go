package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

type Notifier interface {
	Send(alert Alert) error
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

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
// 429) with backoff; other 4xx are returned immediately. It runs synchronously
// in the evaluator goroutine, so the retry budget is kept small (issue #9).
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
			lastErr = err
		} else {
			status := resp.StatusCode
			retryAfter := resp.Header.Get("Retry-After")
			io.Copy(io.Discard, resp.Body) // drain so the connection can be reused
			resp.Body.Close()

			if status >= 200 && status < 300 {
				return nil
			}
			lastErr = fmt.Errorf("webhook %s returned status %d", url, status)

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
	return postJSON(s.WebhookURL, map[string]any{
		"attachments": []map[string]any{{
			"color":    v.slackColor,
			"text":     formatAlert(alert),
			"fallback": formatAlert(alert),
		}},
	})
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

type GenericWebhookNotifier struct {
	WebhookURL string
}

// GenericWebhookNotifier POSTs all alert fields as structured JSON for custom integrations.
func (g *GenericWebhookNotifier) Send(alert Alert) error {
	v := visualFor(alert)
	status := "firing"
	if !alert.IsFiring {
		status = "resolved"
	}
	return postJSON(g.WebhookURL, map[string]any{
		"status":    status,
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
