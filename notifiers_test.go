package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockingNotifier struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingNotifier() *blockingNotifier {
	return &blockingNotifier{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (n *blockingNotifier) Send(Alert) error {
	n.once.Do(func() { close(n.started) })
	<-n.release
	return nil
}

type recordingNotifier struct {
	mu     sync.Mutex
	alerts []Alert
	sent   chan struct{}
}

func newRecordingNotifier() *recordingNotifier {
	return &recordingNotifier{sent: make(chan struct{}, 10)}
}

func (n *recordingNotifier) Send(alert Alert) error {
	n.mu.Lock()
	n.alerts = append(n.alerts, alert)
	n.mu.Unlock()
	n.sent <- struct{}{}
	return nil
}

func (n *recordingNotifier) snapshot() []Alert {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]Alert(nil), n.alerts...)
}

func waitForSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notifier")
	}
}

func TestAsyncNotifierPreservesOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recorder := newRecordingNotifier()
	notifier := newAsyncNotifier([]Notifier{recorder}, 10)
	go notifier.Run(ctx)

	firing := Alert{IsFiring: true, Metric: "memory.used_percent"}
	resolved := Alert{IsFiring: false, Metric: "memory.used_percent"}
	if err := notifier.Send(firing); err != nil {
		t.Fatal(err)
	}
	if err := notifier.Send(resolved); err != nil {
		t.Fatal(err)
	}

	waitForSignal(t, recorder.sent)
	waitForSignal(t, recorder.sent)

	alerts := recorder.snapshot()
	if len(alerts) != 2 {
		t.Fatalf("expected 2 alerts, got %+v", alerts)
	}
	if !alerts[0].IsFiring || alerts[1].IsFiring {
		t.Fatalf("alerts were delivered out of order: %+v", alerts)
	}
}

func TestAsyncNotifierDoesNotBlockWhenQueueIsFull(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	blocking := newBlockingNotifier()
	notifier := newAsyncNotifier([]Notifier{blocking}, 1)
	go notifier.Run(ctx)

	if err := notifier.Send(Alert{Metric: "first"}); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, blocking.started)

	if err := notifier.Send(Alert{Metric: "second"}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err := notifier.Send(Alert{Metric: "third"})
	if !errors.Is(err, errNotifierQueueFull) {
		t.Fatalf("expected queue full error, got %v", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("Send blocked while notifier queue was full")
	}

	close(blocking.release)
}

func notifierTestAlert() Alert {
	return Alert{
		IsFiring:  true,
		Title:     "High memory usage",
		Value:     91.5,
		Threshold: 80,
		Hostname:  "host-a",
		Metric:    "memory.used_percent",
		Unit:      "percent",
		Severity:  SeverityCritical,
	}
}

func captureWebhookPayload(t *testing.T, send func(url string) error) map[string]any {
	t.Helper()

	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := send(server.URL); err != nil {
		t.Fatal(err)
	}
	if payload == nil {
		t.Fatal("expected webhook payload")
	}
	return payload
}

func TestGoogleChatNotifierPayload(t *testing.T) {
	payload := captureWebhookPayload(t, func(url string) error {
		return (&GoogleChatNotifier{WebhookURL: url}).Send(notifierTestAlert())
	})

	text, ok := payload["text"].(string)
	if !ok || text == "" {
		t.Fatalf("expected text payload, got %+v", payload)
	}
	if want := "memory.used_percent"; !strings.Contains(text, want) {
		t.Fatalf("text %q did not contain %q", text, want)
	}
}

func TestDiscordNotifierPayload(t *testing.T) {
	payload := captureWebhookPayload(t, func(url string) error {
		return (&DiscordNotifier{WebhookURL: url}).Send(notifierTestAlert())
	})

	embeds, ok := payload["embeds"].([]any)
	if !ok || len(embeds) != 1 {
		t.Fatalf("expected one embed, got %+v", payload)
	}
	embed, ok := embeds[0].(map[string]any)
	if !ok {
		t.Fatalf("expected embed object, got %+v", embeds[0])
	}
	if got := embed["color"]; got != float64(15158332) {
		t.Fatalf("discord color = %v, want critical color", got)
	}
	description, _ := embed["description"].(string)
	if !strings.Contains(description, "High memory usage") {
		t.Fatalf("description %q missing alert title", description)
	}
}

func TestSlackNotifierPayload(t *testing.T) {
	payload := captureWebhookPayload(t, func(url string) error {
		return (&SlackNotifier{WebhookURL: url}).Send(notifierTestAlert())
	})

	attachments, ok := payload["attachments"].([]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("expected one attachment, got %+v", payload)
	}
	attachment, ok := attachments[0].(map[string]any)
	if !ok {
		t.Fatalf("expected attachment object, got %+v", attachments[0])
	}
	if got := attachment["color"]; got != "danger" {
		t.Fatalf("slack color = %v, want danger", got)
	}
	text, _ := attachment["text"].(string)
	if !strings.Contains(text, "host-a") {
		t.Fatalf("text %q missing hostname", text)
	}
}

func TestGenericWebhookNotifierPayload(t *testing.T) {
	payload := captureWebhookPayload(t, func(url string) error {
		return (&GenericWebhookNotifier{WebhookURL: url}).Send(notifierTestAlert())
	})

	want := map[string]any{
		"status":    "firing",
		"severity":  "critical",
		"title":     "High memory usage",
		"metric":    "memory.used_percent",
		"value":     91.5,
		"threshold": 80.0,
		"unit":      "percent",
		"hostname":  "host-a",
		"color":     "#E74C3C",
	}
	for key, value := range want {
		if got := payload[key]; got != value {
			t.Fatalf("%s = %v, want %v in %+v", key, got, value, payload)
		}
	}
	if text, _ := payload["text"].(string); !strings.Contains(text, "memory.used_percent") {
		t.Fatalf("text %q missing metric", text)
	}
}

func TestTelegramNotifierPayload(t *testing.T) {
	transport := &captureTransport{t: t}
	originalClient := httpClient
	httpClient = &http.Client{Transport: transport}
	t.Cleanup(func() { httpClient = originalClient })

	err := (&TelegramNotifier{BotToken: "token-123", ChatID: "chat-456"}).Send(notifierTestAlert())
	if err != nil {
		t.Fatal(err)
	}

	if transport.url != "https://api.telegram.org/bottoken-123/sendMessage" {
		t.Fatalf("telegram URL = %q", transport.url)
	}
	if got := transport.payload["chat_id"]; got != "chat-456" {
		t.Fatalf("chat_id = %v, want chat-456", got)
	}
	text, _ := transport.payload["text"].(string)
	if !strings.Contains(text, "High memory usage") {
		t.Fatalf("text %q missing alert title", text)
	}
}

type captureTransport struct {
	t       *testing.T
	url     string
	payload map[string]any
}

func (t *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.url = req.URL.String()
	if req.Method != http.MethodPost {
		t.t.Fatalf("method = %s, want POST", req.Method)
	}
	if err := json.NewDecoder(req.Body).Decode(&t.payload); err != nil {
		t.t.Fatal(err)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(http.NoBody),
	}, nil
}
