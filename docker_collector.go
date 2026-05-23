package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
)

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
			case events.ActionDie:
				dockerEventsChannel <- DockerEvent{
					ID:         event.Actor.ID,
					Timestamp:  time.Unix(event.Time, 0),
					Action:     string(events.ActionDie),
					Attributes: event.Actor.Attributes,
				}
			case events.ActionRestart:
				dockerEventsChannel <- DockerEvent{
					ID:         event.Actor.ID,
					Timestamp:  time.Unix(event.Time, 0),
					Action:     string(events.ActionRestart),
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
