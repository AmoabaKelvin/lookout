package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
)

func listenDockerEvents(cli *client.Client, ctx context.Context, f client.Filters, dockerEventsChannel chan<- DockerEvent, lastEventTime *time.Time) error {
	opts := client.EventsListOptions{Filters: f}
	if !lastEventTime.IsZero() {
		opts.Since = fmt.Sprintf("%d.%09d", lastEventTime.Unix(), lastEventTime.Nanosecond())
	}
	result := cli.Events(ctx, opts)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case event, ok := <-result.Messages:
			if !ok {
				return io.EOF
			}

			eventTime := time.Unix(event.Time, event.TimeNano%1_000_000_000)
			*lastEventTime = eventTime

			name := event.Actor.Attributes["name"]
			timestamp := eventTime.Format("15:04:05")
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
				return io.EOF
			}
			return err
		}
	}
}

func dockerCollector(cli *client.Client, ctx context.Context, dockerEventsChannel chan<- DockerEvent) {
	f := make(client.Filters).Add("type", "container")
	fmt.Println("Listening to Docker container events...")

	var lastEventTime time.Time

	for ctx.Err() == nil {
		err := listenDockerEvents(cli, ctx, f, dockerEventsChannel, &lastEventTime)
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
