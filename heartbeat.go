// this is going to be a heartbeat, we get a url and an interval and we just
// ping that url at the intervals that come from the config
// heartbeat is just a way to send an "I am alive" signal to an external
// service so that we don't assume everything is working when we don't hear
// from the agent on the server, for now, its any url that someone can provide,
// in the future though we should have the setup so that each installation script
// can connect to our in-house agent health center without managing other providers
// at the moment, using healthchecks.io is recommended

package main

import "fmt"

func PingRemote(addr string) error {
	resp, err := httpClient.Get(addr)
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("healthcheck failed with status: %s", resp.Status)
	}
	return nil
}
