# Lookout

Lookout is a small monitoring agent for Linux servers. It runs on the machine
you care about, watches the basics, and sends an alert when something crosses a
line or stops reporting.

It is meant for the kind of server where you want useful alerts without running
a full Prometheus stack. One static binary, one YAML file, one systemd service.

## What it watches

Lookout collects from the local machine and evaluates alerts in-process.

| Area | What Lookout checks |
| --- | --- |
| Memory | Used memory percentage, based on `/proc/meminfo` and `MemAvailable` |
| Swap | Used swap percentage |
| Disk | Used percentage for configured mount points |
| Disk growth | Whether a disk is on pace to fill within a configured window |
| Load | 1-minute load average divided by CPU cores |
| CPU | CPU usage percentage from `/proc/stat` |
| systemd | Whether named services are active |
| HTTP | Whether configured URLs return the expected status code |
| TCP | Whether configured TCP addresses accept a connection |
| Processes | Whether named processes are present in `/proc` |
| Docker | Optional container exit, OOM, restart-loop, and healthcheck alerts |
| Heartbeat | Optional dead-man's-switch ping to a service like healthchecks.io |

Alerts can go to Google Chat, Discord, Slack, Microsoft Teams, Telegram,
PagerDuty, a generic webhook, or email. If no notifier is configured, alerts are
printed to stdout, which means they land in the systemd journal when installed
as a service.

## Install

On a Linux server with systemd:

```sh
curl -fsSL https://raw.githubusercontent.com/AmoabaKelvin/lookout/main/install.sh | sudo sh
```

The installer:

- downloads the latest GitHub release for your CPU architecture
- verifies the release checksum
- installs the binary to `/usr/local/bin/lookout`
- creates an unprivileged `lookout` system user
- writes `/etc/lookout/config.yaml` if it does not already exist
- installs and starts `lookout.service`

To pin a specific release:

```sh
curl -fsSL https://raw.githubusercontent.com/AmoabaKelvin/lookout/main/install.sh | sudo VERSION=v1.0.0 sh
```

Re-running the installer upgrades the binary and keeps your existing config.

## Configure

The installed config lives at:

```sh
/etc/lookout/config.yaml
```

After changing it, restart the service:

```sh
sudo systemctl restart lookout
```

A minimal useful config might look like this:

```yaml
collection_interval: 30s
state_file: /var/lib/lookout/state.json

metrics:
  enabled: false
  listen: "127.0.0.1:9100"

alerts:
  renotify_after: 1h
  stale_after: 90s
  memory:
    threshold: 85
    resolve_below: 80
    for: 2m
    severity: warning
  disk:
    threshold: 85
    resolve_below: 80
    for: 2m
    severity: warning
    predict_full_within: 4h
    mounts:
      - /
  load:
    threshold: 1.5
    resolve_below: 1.0
    for: 2m
    severity: warning
  cpu:
    threshold: 85
    resolve_below: 80
    for: 2m
    severity: warning
  swap:
    threshold: 80
    resolve_below: 75
    for: 2m
    severity: warning
  systemd:
    severity: critical
    services:
      - nginx
      - postgresql
  http:
    severity: critical
    checks:
      - name: app
        url: "https://example.com/health"
        timeout: 5s
        expected_status: 200
  tcp:
    severity: critical
    checks:
      - name: redis
        address: "127.0.0.1:6379"
        timeout: 5s
  process:
    severity: critical
    names:
      - nginx

notifiers:
  slack:
    webhook_url: "https://hooks.slack.com/services/XXX/YYY/ZZZ"

heartbeat:
  interval: 60s

docker:
  enabled: false
  severity: critical
  restart_threshold: 3
  restart_window: 10m
```

The full example config is in
[`deploy/config.example.yaml`](deploy/config.example.yaml).

### Thresholds and recovery

For threshold checks, `threshold` is the point where an alert starts and
`resolve_below` is the point where it clears. Keeping those separate prevents
alerts from firing and resolving over and over when a value sits near the line.

The `for` field controls how long a value must stay over the threshold before
Lookout sends the first alert. Set it to `0s` if you want immediate alerts.

`renotify_after` controls repeat notifications while a problem is still firing.

`stale_after` controls alerts for metrics that stop reporting. By default, it is
three times the collection interval.

Durations use Go-style strings such as `5s`, `2m`, and `1h`.

### Prometheus metrics

Lookout can expose the latest collected values as Prometheus text at
`GET /metrics`. This is disabled by default and keeps only the current in-memory
snapshot; it does not add storage, retention, dashboards, or a query layer.

```yaml
metrics:
  enabled: true
  listen: "127.0.0.1:9100"
```

Keep the listener on localhost or a private interface unless you place it behind
a firewall or reverse proxy.

### Severity

Severity can be `warning` or `critical`. It affects the label, color, and
PagerDuty severity used for the alert.

## Notifiers

Lookout sends an alert to every notifier you configure.

```yaml
notifiers:
  google_chat:
    webhook_url: "https://chat.googleapis.com/v1/spaces/XXX/messages?key=...&token=..."
  discord:
    webhook_url: "https://discord.com/api/webhooks/XXX/YYY"
  slack:
    webhook_url: "https://hooks.slack.com/services/XXX/YYY/ZZZ"
  teams:
    webhook_url: "https://example.webhook.office.com/..."
  telegram:
    bot_token: "123456:ABC..."
    chat_id: "987654321"
  pagerduty:
    integration_key: "pagerduty-events-api-v2-key"
  webhook:
    webhook_url: "https://example.com/hooks/lookout"
  email:
    host: "smtp.example.com"
    port: 587
    implicit_tls: false
    username: ""
    password: ""
    from: "lookout@example.com"
    to:
      - "you@example.com"
```

The generic webhook receives structured JSON with the alert status, severity,
title, metric, value, threshold, unit, hostname, color, and rendered text.

Webhook URLs are redacted in logs so tokens in paths or query strings are not
printed on send failures.

## Docker monitoring

Docker monitoring is off by default.

```yaml
docker:
  enabled: true
  severity: critical
  restart_threshold: 3
  restart_window: 10m
```

When enabled, Lookout listens to Docker container events and alerts on:

- non-zero container exits
- containers killed by OOM
- restart loops
- unhealthy Docker healthchecks

Intentional stops are ignored. Quick restarts are debounced so a normal restart
does not immediately look like a failed container.

The service user must be able to read Docker events. On many systems that means
adding it to the `docker` group and restarting the service:

```sh
sudo usermod -aG docker lookout
sudo systemctl restart lookout
```

## Heartbeat

Lookout can ping a dead-man's-switch URL on a fixed interval:

```yaml
heartbeat:
  url: "https://hc-ping.com/your-uuid"
  interval: 60s
```

This is useful for catching the cases where the whole server, network, or
Lookout service disappears and therefore cannot send its own alert.

## Operating the service

Check status:

```sh
sudo systemctl status lookout
```

Watch logs:

```sh
sudo journalctl -u lookout -f
```

Restart after changing config:

```sh
sudo systemctl restart lookout
```

Run manually with a specific config:

```sh
sudo lookout --config /etc/lookout/config.yaml
```

## Uninstall

Remove the service and binary, but keep config and the service user:

```sh
curl -fsSL https://raw.githubusercontent.com/AmoabaKelvin/lookout/main/uninstall.sh | sudo sh
```

Remove the service, binary, config directory, and service user:

```sh
curl -fsSL https://raw.githubusercontent.com/AmoabaKelvin/lookout/main/uninstall.sh | sudo sh -s -- --purge
```

## Build from source

You need Go installed.

Run the tests:

```sh
go test ./...
```

Build a local binary:

```sh
go build -o lookout .
```

Build release artifacts for Linux amd64 and arm64:

```sh
sh build.sh
```

Artifacts are written to `dist/`, along with `checksums.txt`.

Tagged releases are built by GitHub Actions when a tag like `v1.0.0` is pushed.

## Notes

Lookout is Linux-first. The collectors read Linux kernel files such as
`/proc/meminfo`, `/proc/loadavg`, `/proc/stat`, and `/proc/mounts`, and the
installer creates a systemd service.

The agent stores alert state under `/var/lib/lookout` by default. That state is
what lets it remember firing alerts across restarts and send clean recovery
messages instead of starting from scratch every time the service restarts.
