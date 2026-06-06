package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Notifier interface {
	Send(alert Alert) error
}

// buildNotifiers turns the configured notifier sections into live notifiers. A
// nil section (absent from the config) or one missing its credentials is skipped.
func buildNotifiers(cfg NotifiersConfig) []Notifier {
	var notifiers []Notifier
	if n := cfg.GoogleChat; n != nil && n.WebhookURL != "" {
		notifiers = append(notifiers, &GoogleChatNotifier{WebhookURL: n.WebhookURL})
	}
	if n := cfg.Discord; n != nil && n.WebhookURL != "" {
		notifiers = append(notifiers, &DiscordNotifier{WebhookURL: n.WebhookURL})
	}
	if n := cfg.Slack; n != nil && n.WebhookURL != "" {
		notifiers = append(notifiers, &SlackNotifier{WebhookURL: n.WebhookURL})
	}
	if n := cfg.Webhook; n != nil && n.URL != "" {
		notifiers = append(notifiers, &GenericWebhookNotifier{WebhookURL: n.URL})
	}
	if n := cfg.Telegram; n != nil && n.BotToken != "" && n.ChatID != "" {
		notifiers = append(notifiers, &TelegramNotifier{BotToken: n.BotToken, ChatID: n.ChatID})
	}
	if n := cfg.Email; n != nil && n.Host != "" && n.From != "" && len(n.To) > 0 {
		notifiers = append(notifiers, &SMTPNotifier{
			Host: n.Host, Port: n.Port,
			Username: n.Username, Password: n.Password,
			From: n.From, To: n.To,
		})
	}
	return notifiers
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
	if ue, ok := err.(*url.Error); ok {
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
			lastErr = fmt.Errorf("%s: %v", safeURL(url), unwrapURL(err))
		} else {
			status := resp.StatusCode
			retryAfter := resp.Header.Get("Retry-After")
			io.Copy(io.Discard, resp.Body) // drain so the connection can be reused
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
