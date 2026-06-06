package main

import "fmt"

// PingRemote sends an "I'm alive" ping to an external dead-man's-switch service
// (e.g. healthchecks.io). When the pings stop, that service alerts us.
func PingRemote(addr string) error {
	resp, err := httpClient.Get(addr)
	if err != nil {
		return fmt.Errorf("%s: %v", safeURL(addr), unwrapURL(err))
	}

	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("healthcheck %s failed with status: %s", safeURL(addr), resp.Status)
	}
	return nil
}
