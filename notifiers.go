package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Notifier interface {
	Send(alert Alert) error
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

func formatAlert(alert Alert) string {
	status := "🔴 FIRING"
	if !alert.IsFiring {
		status = "✅ RESOLVED"
	}
	return fmt.Sprintf("%s — %s\nHost: %s\nMetric: %s\nValue: %s (threshold %s)",
		status, alert.Title, alert.Hostname, alert.Metric,
		formatValue(alert.Value, alert.Unit), formatValue(alert.Threshold, alert.Unit))
}

// formatValue renders a value with its unit: percentages as "6.68%", everything
// else with a space ("512.00 kB").
func formatValue(value float64, unit string) string {
	if unit == "percent" {
		return fmt.Sprintf("%.2f%%", value)
	}
	return fmt.Sprintf("%.2f %s", value, unit)
}

func postJSON(url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook %s returned status %d", url, resp.StatusCode)
	}
	return nil
}

type GoogleChatNotifier struct {
	WebhookURL string
}

type ConsoleNotifier struct{}

type DiscordNotifier struct {
	WebhookURL string
}

// Discord incoming webhooks expect a JSON body with a "content" field.
func (d *DiscordNotifier) Send(alert Alert) error {
	return postJSON(d.WebhookURL, map[string]string{"content": formatAlert(alert)})
}

// Google Chat incoming webhooks expect a JSON body with a "text" field.
func (g *GoogleChatNotifier) Send(alert Alert) error {
	return postJSON(g.WebhookURL, map[string]string{"text": formatAlert(alert)})
}

func (c *ConsoleNotifier) Send(alert Alert) error {
	fmt.Println(formatAlert(alert))
	return nil
}
