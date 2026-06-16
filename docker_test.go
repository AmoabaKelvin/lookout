package main

import (
	"testing"
	"time"

	"github.com/moby/moby/api/types/events"
)

func TestDockerMessageTimePrefersNanoseconds(t *testing.T) {
	event := events.Message{
		Time:     100,
		TimeNano: 200_000_000_003,
	}

	got := dockerMessageTime(event)
	want := time.Unix(200, 3)
	if !got.Equal(want) {
		t.Fatalf("dockerMessageTime = %s, want %s", got, want)
	}
}

func TestDockerMessageTimeFallsBackToSeconds(t *testing.T) {
	event := events.Message{Time: 100}

	got := dockerMessageTime(event)
	want := time.Unix(100, 0)
	if !got.Equal(want) {
		t.Fatalf("dockerMessageTime = %s, want %s", got, want)
	}
}

func TestDockerEventSinceUsesNanosecondTimestamp(t *testing.T) {
	ts := time.Unix(100, 42)

	if got := dockerEventSince(ts); got != "100.000000042" {
		t.Fatalf("dockerEventSince = %q", got)
	}
}

func TestSkipDockerReplayEventSkipsBoundaryDuplicate(t *testing.T) {
	since := time.Unix(100, 42)

	if !skipDockerReplayEvent(since, since) {
		t.Fatal("expected exact boundary event to be skipped")
	}
	if !skipDockerReplayEvent(since, since.Add(-time.Nanosecond)) {
		t.Fatal("expected older replay event to be skipped")
	}
	if skipDockerReplayEvent(since, since.Add(time.Nanosecond)) {
		t.Fatal("expected newer replay event to be kept")
	}
}
