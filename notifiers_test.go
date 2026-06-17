package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
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

func TestBuildNotifiersValidatesAndReturnsActiveNames(t *testing.T) {
	notifiers, active, err := buildNotifiers(NotifiersConfig{
		Slack:    &WebhookConfig{WebhookURL: "https://hooks.slack.com/services/X/Y/Z"},
		Teams:    &WebhookConfig{WebhookURL: "https://example.webhook.office.com/team"},
		Webhook:  &WebhookConfig{WebhookURL: "https://example.com/lookout"},
		Telegram: &TelegramConfig{BotToken: "token", ChatID: "chat"},
		PagerDuty: &PagerDutyConfig{
			IntegrationKey: "pagerduty-key",
		},
		Email: &EmailConfig{
			Host:        "smtp.example.com",
			Port:        587,
			ImplicitTLS: true,
			From:        "lookout@example.com",
			To:          []string{"ops@example.com"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(notifiers) != 6 {
		t.Fatalf("notifiers = %d, want 6", len(notifiers))
	}
	wantActive := []string{"slack", "teams", "webhook", "telegram", "pagerduty", "email"}
	for _, name := range wantActive {
		if !slices.Contains(active, name) {
			t.Fatalf("active notifiers = %v, missing %s", active, name)
		}
	}
	smtpNotifier := findSMTPNotifier(notifiers)
	if smtpNotifier == nil || !smtpNotifier.ImplicitTLS {
		t.Fatalf("email notifier did not keep implicit TLS setting: %#v", notifiers)
	}
}

func findSMTPNotifier(notifiers []Notifier) *SMTPNotifier {
	for _, notifier := range notifiers {
		if smtpNotifier, ok := notifier.(*SMTPNotifier); ok {
			return smtpNotifier
		}
	}
	return nil
}

func TestBuildNotifiersRejectsInvalidWebhookURL(t *testing.T) {
	_, _, err := buildNotifiers(NotifiersConfig{
		Slack: &WebhookConfig{WebhookURL: "not-a-url"},
	})
	if err == nil || !strings.Contains(err.Error(), "notifiers.slack.webhook_url") {
		t.Fatalf("expected slack webhook validation error, got %v", err)
	}
}

func TestBuildNotifiersRejectsIncompleteTelegram(t *testing.T) {
	_, _, err := buildNotifiers(NotifiersConfig{
		Telegram: &TelegramConfig{BotToken: "token"},
	})
	if err == nil || !strings.Contains(err.Error(), "notifiers.telegram.chat_id") {
		t.Fatalf("expected telegram chat_id validation error, got %v", err)
	}
}

func TestBuildNotifiersRejectsIncompletePagerDuty(t *testing.T) {
	_, _, err := buildNotifiers(NotifiersConfig{
		PagerDuty: &PagerDutyConfig{},
	})
	if err == nil || !strings.Contains(err.Error(), "notifiers.pagerduty.integration_key") {
		t.Fatalf("expected pagerduty integration_key validation error, got %v", err)
	}
}

func TestBuildNotifiersRejectsInvalidEmailPort(t *testing.T) {
	_, _, err := buildNotifiers(NotifiersConfig{
		Email: &EmailConfig{
			Host: "smtp.example.com",
			From: "lookout@example.com",
			To:   []string{"ops@example.com"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "notifiers.email.port") {
		t.Fatalf("expected email port validation error, got %v", err)
	}
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

func TestTeamsNotifierPayload(t *testing.T) {
	payload := captureWebhookPayload(t, func(url string) error {
		return (&TeamsNotifier{WebhookURL: url}).Send(notifierTestAlert())
	})

	if got := payload["type"]; got != "message" {
		t.Fatalf("type = %v, want message", got)
	}
	attachments, ok := payload["attachments"].([]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("expected one attachment, got %+v", payload)
	}
	attachment, _ := attachments[0].(map[string]any)
	content, _ := attachment["content"].(map[string]any)
	if content["type"] != "AdaptiveCard" {
		t.Fatalf("expected adaptive card content, got %+v", content)
	}
	body, _ := content["body"].([]any)
	if len(body) == 0 {
		t.Fatalf("expected adaptive card body, got %+v", content)
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

func TestPagerDutyNotifierPayload(t *testing.T) {
	transport := &captureTransport{t: t}
	originalClient := httpClient
	httpClient = &http.Client{Transport: transport}
	t.Cleanup(func() { httpClient = originalClient })

	err := (&PagerDutyNotifier{IntegrationKey: "routing-key"}).Send(notifierTestAlert())
	if err != nil {
		t.Fatal(err)
	}

	if transport.url != "https://events.pagerduty.com/v2/enqueue" {
		t.Fatalf("pagerduty URL = %q", transport.url)
	}
	if got := transport.payload["routing_key"]; got != "routing-key" {
		t.Fatalf("routing_key = %v", got)
	}
	if got := transport.payload["event_action"]; got != "trigger" {
		t.Fatalf("event_action = %v", got)
	}
	if got := transport.payload["dedup_key"]; got != "host-a:memory.used_percent" {
		t.Fatalf("dedup_key = %v", got)
	}
	payload, _ := transport.payload["payload"].(map[string]any)
	if got := payload["severity"]; got != "critical" {
		t.Fatalf("severity = %v", got)
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

func TestSMTPNotifierSendsWithImplicitTLS(t *testing.T) {
	host, port, messages, closeServer := startImplicitTLSSMTPServer(t)
	defer closeServer()
	originalTLSConfig := smtpTLSConfig
	smtpTLSConfig = func(host string) *tls.Config {
		return &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host, InsecureSkipVerify: true}
	}
	t.Cleanup(func() { smtpTLSConfig = originalTLSConfig })

	err := (&SMTPNotifier{
		Host:        host,
		Port:        port,
		ImplicitTLS: true,
		From:        "lookout@example.com",
		To:          []string{"ops@example.com"},
	}).Send(notifierTestAlert())
	if err != nil {
		t.Fatal(err)
	}

	select {
	case msg := <-messages:
		if !strings.Contains(msg, "High memory usage") {
			t.Fatalf("message missing alert title: %q", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SMTP message")
	}
}

func TestSMTPNotifierPort465UsesImplicitTLS(t *testing.T) {
	notifier := &SMTPNotifier{Port: 465}
	if !notifier.usesImplicitTLS() {
		t.Fatal("port 465 should use implicit TLS")
	}
}

func startImplicitTLSSMTPServer(t *testing.T) (string, int, <-chan string, func()) {
	t.Helper()

	cert := testTLSCertificate(t)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}

	messages := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)
		writeSMTPLine(t, writer, "220 localhost ESMTP")
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"):
				writeSMTPLine(t, writer, "250 localhost")
			case strings.HasPrefix(cmd, "MAIL FROM:"):
				writeSMTPLine(t, writer, "250 OK")
			case strings.HasPrefix(cmd, "RCPT TO:"):
				writeSMTPLine(t, writer, "250 OK")
			case cmd == "DATA":
				writeSMTPLine(t, writer, "354 End data with <CR><LF>.<CR><LF>")
				var msg strings.Builder
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimSpace(line) == "." {
						break
					}
					msg.WriteString(line)
				}
				messages <- msg.String()
				writeSMTPLine(t, writer, "250 OK")
			case cmd == "QUIT":
				writeSMTPLine(t, writer, "221 Bye")
				return
			default:
				writeSMTPLine(t, writer, "250 OK")
			}
		}
	}()

	addr := listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, messages, func() {
		listener.Close()
		<-done
	}
}

func writeSMTPLine(t *testing.T, writer *bufio.Writer, line string) {
	t.Helper()
	if _, err := writer.WriteString(line + "\r\n"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
}

func testTLSCertificate(t *testing.T) tls.Certificate {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER := x509.MarshalPKCS1PrivateKey(key)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert
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
