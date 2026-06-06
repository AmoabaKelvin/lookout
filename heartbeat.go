package main

import "fmt"

// PingRemote sends an "I'm alive" ping to an external dead-man's-switch service
// (e.g. healthchecks.io). When the pings stop, that service alerts us.
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
