package main

import (
	"context"
	"errors"
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
