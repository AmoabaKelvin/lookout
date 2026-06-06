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
	Attributes map[string]string // this will have info about say the image, exit code
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

	for ctx.Err() == nil {
		err := listenDockerEvents(cli, ctx, f, dockerEventsChannel)
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Printf("[docker] event stream error: %v\n", err)
		}

		// we are listening to the events from the docker daemon, the reason we are
		// sleeping here is so if there's any issue with the daemon, we try again
		// after some time in order to resume listening to the events
		// we will be looking at a better way to do this though.
		// issue here is after sleeping for 2 seconds, assuming there were events
		// that happened during that time, we will miss them all. so we need to always
		// start checking / listening to events for every 2 seconds before.
		time.Sleep(2 * time.Second)
	}
}

func listenDockerEvents(cli *client.Client, ctx context.Context, f client.Filters, dockerEventsChannel chan<- DockerEvent) error {
	result := cli.Events(ctx, client.EventsListOptions{Filters: f})

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case event, ok := <-result.Messages:
			if !ok {
				return io.EOF
			}

			name := event.Actor.Attributes["name"]
			timestamp := time.Unix(event.Time, 0).Format("15:04:05")
			fmt.Printf("[docker] [%s] %s\n", timestamp, name)

			switch event.Action {
			case events.ActionDie, events.ActionOOM, events.ActionRestart, events.ActionStart, events.ActionStop:
				dockerEventsChannel <- DockerEvent{
					ID:         event.Actor.ID,
					Timestamp:  time.Unix(event.Time, 0),
					Action:     string(event.Action),
					Attributes: event.Actor.Attributes,
				}
			}

		case err, ok := <-result.Err:
			if !ok || err == nil {
				return io.EOF
			}
			return err
		}
	}
}

// handleDockerEvent updates the tracked state for a container from a single
// docker event. A "die" is debounced through evalCh so a quick restart can
// cancel the pending failure evaluation.
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
		// container.HasOOM, container.HasStopped, container.PendingDie = false, false, false
		// evaluateContainer(container)

		if container.PendingDie && container.LastDieAt.Before(container.StartedAt) {
			// we should mark as resolved
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

		// a way to trigger an evaluation after X seconds after the die has happened
		id := dockerEvent.ID
		time.AfterFunc(1500*time.Millisecond, func() {
			evalCh <- id
		})
	case "stop":
		container.LastStopAt = dockerEvent.Timestamp
	case "oom":
		container.LastOOMAt = dockerEvent.Timestamp
		// FIX: we will have the correct alerts for this later
		fmt.Printf("The container %s was killed because of an OOM", container.ID)
	}
}

func evaluateContainer(c *ContainerState) {
	if c.PendingDie {
		if !c.LastOOMAt.IsZero() {
			// died with an OOM
			fmt.Printf("the container %s died with an OOM, immediate alert", c.ID)
		} else if !c.LastStopAt.IsZero() && c.LastExit != 0 {
			fmt.Printf("the container %s did not die cleanly", c.ID)
		}

		if c.LastExit != 0 {
			// the container did not die cleanly
			fmt.Printf("The container did not die cleanly")
		}
	}

	c.PendingDie = false
	c.LastOOMAt = time.Time{}
	c.LastStopAt = time.Time{}
}
