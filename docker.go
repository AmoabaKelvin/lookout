package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
)

type DockerEvent struct {
	ID         string
	Timestamp  time.Time
	Action     string
	Attributes map[string]string
}

type State string

const (
	Running State = "running"
	Exited  State = "stopped"
	Paused  State = "paused"
	Created State = "created"
	Removed State = "removed"
)

type ContainerState struct {
	ID         string
	Name       string
	Image      string
	State      State
	LastOOMAt  time.Time
	LastExit   int
	LastStopAt time.Time
	LastDieAt  time.Time
	PendingDie bool
	StartedAt  time.Time
	DieTimes   []time.Time
}

func dockerCollector(cli *client.Client, ctx context.Context, dockerEventsChannel chan<- DockerEvent) {
	f := make(client.Filters).Add("type", "container")
	fmt.Println("Listening to Docker container events...")

	var lastSeen time.Time
	for ctx.Err() == nil {
		latest, err := listenDockerEvents(cli, ctx, f, lastSeen, dockerEventsChannel)
		if latest.After(lastSeen) {
			lastSeen = latest
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Printf("[docker] event stream error: %v\n", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func listenDockerEvents(cli *client.Client, ctx context.Context, f client.Filters, since time.Time, dockerEventsChannel chan<- DockerEvent) (time.Time, error) {
	options := client.EventsListOptions{Filters: f}
	if !since.IsZero() {
		options.Since = dockerEventSince(since)
	}
	result := cli.Events(ctx, options)
	latest := since

	for {
		select {
		case <-ctx.Done():
			return latest, ctx.Err()

		case event, ok := <-result.Messages:
			if !ok {
				return latest, io.EOF
			}

			eventTime := dockerMessageTime(event)
			if !eventTime.IsZero() {
				if skipDockerReplayEvent(since, eventTime) {
					continue
				}
				if eventTime.After(latest) {
					latest = eventTime
				}
			}

			name := event.Actor.Attributes["name"]
			timestamp := "unknown"
			if !eventTime.IsZero() {
				timestamp = eventTime.Format("15:04:05")
			}
			fmt.Printf("[docker] [%s] %s\n", timestamp, name)

			switch event.Action {
			case events.ActionDie, events.ActionOOM, events.ActionRestart, events.ActionStart, events.ActionStop:
				dockerEventsChannel <- DockerEvent{
					ID:         event.Actor.ID,
					Timestamp:  eventTime,
					Action:     string(event.Action),
					Attributes: event.Actor.Attributes,
				}
			}

		case err, ok := <-result.Err:
			if !ok || err == nil {
				return latest, io.EOF
			}
			return latest, err
		}
	}
}

func dockerMessageTime(event events.Message) time.Time {
	if event.TimeNano > 0 {
		return time.Unix(0, event.TimeNano)
	}
	if event.Time > 0 {
		return time.Unix(event.Time, 0)
	}
	return time.Time{}
}

func dockerEventSince(t time.Time) string {
	return fmt.Sprintf("%d.%09d", t.Unix(), t.Nanosecond())
}

func skipDockerReplayEvent(since time.Time, eventTime time.Time) bool {
	return !since.IsZero() && !eventTime.IsZero() && !eventTime.After(since)
}

// handleDockerEvent updates container state from one event. A die is debounced
// via evalCh so a quick restart can cancel the pending failure evaluation.
func handleDockerEvent(containers map[string]*ContainerState, dockerEvent DockerEvent, evalCh chan<- string) {
	container := containers[dockerEvent.ID]
	if container == nil {
		container = &ContainerState{
			ID: dockerEvent.ID,
		}
		containers[dockerEvent.ID] = container
	}

	if name := dockerEvent.Attributes["name"]; name != "" {
		container.Name = name
	}
	if image := dockerEvent.Attributes["image"]; image != "" {
		container.Image = image
	}

	switch dockerEvent.Action {
	case "start", "restart":
		container.State = Running
		container.StartedAt = dockerEvent.Timestamp

		if container.PendingDie && container.LastDieAt.Before(container.StartedAt) {
			container.PendingDie = false
			// TODO: evaluate the restart loop here
		}
	case "die":
		container.State = Exited
		if code, err := strconv.Atoi(dockerEvent.Attributes["exitCode"]); err == nil {
			container.LastExit = code
		}
		container.LastDieAt = dockerEvent.Timestamp
		container.DieTimes = append(container.DieTimes, dockerEvent.Timestamp)
		container.PendingDie = true

		id := dockerEvent.ID
		time.AfterFunc(1500*time.Millisecond, func() {
			evalCh <- id
		})
	case "stop":
		container.LastStopAt = dockerEvent.Timestamp
	case "oom":
		container.LastOOMAt = dockerEvent.Timestamp
		// TODO: proper OOM alert
		fmt.Printf("The container %s was killed because of an OOM", container.ID)
	}
}

func evaluateContainer(c *ContainerState) {
	if c.PendingDie {
		if !c.LastOOMAt.IsZero() {
			fmt.Printf("the container %s died with an OOM, immediate alert", c.ID)
		} else if !c.LastStopAt.IsZero() && c.LastExit != 0 {
			fmt.Printf("the container %s did not die cleanly", c.ID)
		}

		if c.LastExit != 0 {
			fmt.Printf("The container did not die cleanly")
		}
	}

	c.PendingDie = false
	c.LastOOMAt = time.Time{}
	c.LastStopAt = time.Time{}
}
