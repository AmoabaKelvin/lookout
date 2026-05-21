package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
)

func listenDockerEvents(cli *client.Client, ctx context.Context, f client.Filters) error {
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
				if exitCode := event.Actor.Attributes["exitCode"]; exitCode != "" {
					fmt.Printf("[docker] [%s] %s (Killed with exit code %s)\n", timestamp, name, exitCode)
				} else {
					fmt.Printf("[docker] [%s] %s (Killed)\n", timestamp, name)
				}
			case events.ActionRestart:
				fmt.Printf("[docker] [%s] %s (Restarted)\n", timestamp, name)
			}

		case err, ok := <-result.Err:
			if !ok || err == nil {
				return io.EOF
			}
			return err
		}
	}
}
