# PRD: Hosted Heartbeat Monitoring

## Summary

Lookout already supports heartbeat pings through `heartbeat.url`, but today the
user has to bring their own dead-man's-switch service. This feature adds that
service to Lookout Web.

A user should be able to create a monitor in the web app, copy a unique ping
URL, paste it into their Lookout agent config, and get alerted if the agent or
server stops pinging.

This turns heartbeat setup from "go sign up for another platform" into a native
Lookout flow.

## Problem

Lookout can detect problems while it is running, but if the whole server goes
down, the network drops, or the Lookout process stops, the agent cannot send a
normal alert.

The current workaround is external heartbeat monitoring. That works, but it
creates friction:

- users have to understand what a dead-man's switch is
- users have to create an account somewhere else
- setup docs send people away from Lookout
- Lookout has no first-party view of whether installed agents are alive

## Goals

- Let a user create a heartbeat monitor from Lookout Web.
- Generate a unique ping URL for each monitor.
- Show copy-paste config for the Lookout agent.
- Record incoming pings and display current monitor status.
- Mark a monitor down when pings stop arriving within the grace window.
- Send an alert when a monitor changes from up to down.
- Send a recovery alert when pings resume.
- Keep the first version simple enough to ship quickly.

## Non-Goals

- Replacing full uptime monitoring products.
- Public status pages.
- Complex incident workflows.
- Multi-step escalation policies.
- Synthetic HTTP checks from Lookout Web.
- Agent authentication beyond a hard-to-guess monitor token.
- Billing, quotas, or plan limits in the first build unless the product already
  has those systems.

## Target Users

- A developer running a small VPS.
- A founder or solo operator responsible for production servers.
- A team that wants basic server monitoring without running a heavy stack.

## User Story

As a Lookout user, I want to create a heartbeat monitor and paste its ping URL
into my server config, so that Lookout can tell me if my server or monitoring
agent stops checking in.

## Primary Flow

1. User opens Lookout Web.
2. User clicks "New monitor".
3. User chooses "Heartbeat".
4. User enters:
   - monitor name
   - expected ping interval
   - grace period
   - notification channel, if the app supports this already
5. App creates the monitor and shows a unique ping URL.
6. App shows the exact Lookout config snippet:

   ```yaml
   heartbeat:
     url: "https://app.lookout.example/ping/{token}"
     interval: 60s
   ```

7. User pastes the snippet into `/etc/lookout/config.yaml`.
8. User restarts Lookout:

   ```sh
   sudo systemctl restart lookout
   ```

9. First ping arrives.
10. Monitor status changes to `up`.
11. If pings stop arriving, the monitor changes to `down` and alerts the user.
12. If pings resume, the monitor changes back to `up` and sends a recovery alert.

## Monitor States

### Pending

The monitor has been created but has not received a ping yet.

UI copy:

> Waiting for the first ping.

### Up

The monitor has received a ping recently enough.

### Late

The monitor has missed its expected time but is still inside the grace period.
This is useful in the UI, but should not alert yet.

### Down

The monitor has exceeded its grace period. This should create a firing alert.

### Paused

The user temporarily disabled alerting for this monitor. Pings may still be
recorded, but the monitor should not send down or recovery alerts while paused.

## Timing Model

Each monitor has:

- `interval_seconds`: how often the agent is expected to ping
- `grace_seconds`: how much extra time is allowed before alerting
- `last_ping_at`: timestamp of the most recent accepted ping

A monitor is down when:

```text
now > last_ping_at + interval_seconds + grace_seconds
```

Recommended defaults:

- `interval_seconds`: `60`
- `grace_seconds`: `120`

That means a monitor pinging every minute is marked down after roughly three
minutes without a ping.

## Data Model

### monitors

| Field | Type | Notes |
| --- | --- | --- |
| `id` | uuid | Internal monitor id |
| `user_id` | uuid | Owner |
| `name` | text | User-facing name |
| `kind` | text | `heartbeat` for this feature |
| `token_hash` | text | Hash of the ping token |
| `token_prefix` | text | Short prefix for debugging/support |
| `interval_seconds` | integer | Expected ping interval |
| `grace_seconds` | integer | Allowed lateness before down |
| `status` | text | `pending`, `up`, `late`, `down`, `paused` |
| `last_ping_at` | timestamp nullable | Last accepted ping |
| `last_checked_at` | timestamp nullable | Last background evaluation |
| `down_since` | timestamp nullable | When the current outage started |
| `last_alerted_at` | timestamp nullable | Last down alert |
| `created_at` | timestamp | Creation time |
| `updated_at` | timestamp | Last update time |

### monitor_pings

This table is optional for v1, but useful for charts and debugging.

| Field | Type | Notes |
| --- | --- | --- |
| `id` | uuid | Ping id |
| `monitor_id` | uuid | Linked monitor |
| `received_at` | timestamp | Server receive time |
| `remote_ip` | text nullable | Useful for debugging |
| `user_agent` | text nullable | Useful for confirming Lookout agent usage |

Retention can be short. For v1, keep 7 to 30 days.

### monitor_events

Useful for audit history and the activity feed.

| Field | Type | Notes |
| --- | --- | --- |
| `id` | uuid | Event id |
| `monitor_id` | uuid | Linked monitor |
| `type` | text | `created`, `ping_received`, `status_changed`, `alert_sent`, `paused`, `resumed` |
| `from_status` | text nullable | Previous status |
| `to_status` | text nullable | New status |
| `message` | text nullable | Human-readable event text |
| `created_at` | timestamp | Event time |

## API

### Create Monitor

```http
POST /api/monitors
```

Request:

```json
{
  "kind": "heartbeat",
  "name": "Production VPS",
  "interval_seconds": 60,
  "grace_seconds": 120
}
```

Response:

```json
{
  "id": "monitor_uuid",
  "name": "Production VPS",
  "kind": "heartbeat",
  "status": "pending",
  "interval_seconds": 60,
  "grace_seconds": 120,
  "ping_url": "https://app.lookout.example/ping/lk_live_xxx"
}
```

The raw ping token should only be shown immediately after creation or explicit
token rotation. Store only a hash in the database.

### List Monitors

```http
GET /api/monitors
```

Returns monitors owned by the current user.

### Get Monitor

```http
GET /api/monitors/{id}
```

Returns monitor details, recent events, and recent ping history if stored.

### Update Monitor

```http
PATCH /api/monitors/{id}
```

Editable fields:

- `name`
- `interval_seconds`
- `grace_seconds`
- `status` for pause/resume, or separate endpoints if preferred

### Rotate Ping URL

```http
POST /api/monitors/{id}/rotate-token
```

Rotates the token and returns a new ping URL.

Use this if a ping URL was leaked.

### Receive Ping

```http
GET /ping/{token}
POST /ping/{token}
HEAD /ping/{token}
```

All three methods should be accepted. The current Go agent uses `GET`.

Successful response:

```json
{
  "ok": true,
  "status": "up"
}
```

Status codes:

- `200`: ping accepted
- `404`: token not found
- `410`: monitor disabled or token revoked, if that distinction is useful
- `429`: rate limited

On accepted ping:

- update `last_ping_at`
- insert `monitor_pings` row if ping history is enabled
- if current status is `pending`, `late`, or `down`, change status to `up`
- if current status was `down`, send a recovery alert

## Background Job

A scheduled job should evaluate heartbeat monitors regularly.

Recommended cadence:

```text
every 30 seconds
```

For each active heartbeat monitor:

1. Skip paused monitors.
2. If `last_ping_at` is null, keep status `pending`.
3. If now is before `last_ping_at + interval`, status is `up`.
4. If now is after `last_ping_at + interval` but before
   `last_ping_at + interval + grace`, status is `late`.
5. If now is after `last_ping_at + interval + grace`, status is `down`.
6. Send a down alert only when status changes into `down`.

The job must be idempotent. Running it twice should not send duplicate alerts.

## Alerting Rules

### Down Alert

Send when a monitor moves from `up`, `late`, or `pending` into `down`.

Title:

```text
Heartbeat missed: {monitor_name}
```

Body:

```text
Lookout has not received a ping from {monitor_name} since {last_ping_at}.
Expected every {interval_seconds}s with a {grace_seconds}s grace period.
```

### Recovery Alert

Send when a monitor moves from `down` into `up`.

Title:

```text
Heartbeat restored: {monitor_name}
```

Body:

```text
Lookout received a new ping from {monitor_name}. The monitor is healthy again.
```

### Pending Alert Behavior

Do not alert for a newly created monitor that has never pinged. It should remain
`pending` until the first ping arrives.

## UI Requirements

### Monitors List

Show:

- monitor name
- status badge
- last ping time
- expected interval
- next expected ping or overdue duration

Recommended actions:

- view details
- pause/resume
- copy setup snippet

### Create Monitor Page

Fields:

- name
- expected interval
- grace period

Helpful defaults:

- interval: 60 seconds
- grace: 120 seconds

After creation, show:

- ping URL
- copy button for URL
- copy button for YAML snippet
- restart command

Do not bury the setup snippet. This is the moment where the user is trying to
finish installation.

### Monitor Detail Page

Show:

- current status
- ping URL setup instructions
- last ping time
- down since, if down
- recent events
- recent pings, if stored
- token rotation action
- pause/resume action

## Security

- Ping tokens must be long, random, and unguessable.
- Store a hash of the token, not the raw token.
- Show the raw token only when created or rotated.
- Treat the ping URL like a secret.
- Rate limit ping endpoints by token and IP.
- Do not expose whether a token belongs to a particular user.
- Avoid logging full ping URLs.
- If using multi-tenant data, every monitor read/update endpoint must enforce
  ownership by authenticated user.

Suggested token format:

```text
lk_ping_{random_32_plus_bytes}
```

Store:

- `token_hash`
- `token_prefix`, for support/debugging only

## Edge Cases

- Duplicate pings should be harmless.
- Very frequent pings should be rate-limited but should not break the monitor.
- Clock skew on the user's server does not matter because the app uses receive
  time, not client time.
- A monitor that has never pinged should not alert.
- A paused monitor should not send down or recovery alerts.
- If a down monitor is deleted, do not send recovery.
- Token rotation should invalidate the old ping URL.
- If notification sending fails, record that failure and retry according to the
  app's existing notification system.

## Success Metrics

- Users can create a heartbeat monitor and get a ping URL in under one minute.
- At least one ping is received within five minutes of monitor creation for most
  completed setup flows.
- Down alerts are sent within one job interval after the grace period expires.
- Recovery alerts are sent immediately after a new ping arrives.
- Support questions about third-party heartbeat setup decrease.

## V1 Scope

Ship this first:

- authenticated monitor creation
- unique ping URL
- ping ingestion endpoint
- monitor list and detail pages
- setup snippet for Lookout config
- background status evaluation
- down and recovery alerts through the app's existing notification channel
- pause/resume
- token rotation

Defer:

- ping charts
- public status pages
- team routing rules
- per-monitor notification overrides
- incident timelines beyond simple events
- custom alert templates

## Open Questions

- What notification channels already exist in `lookout-web`?
- Does `lookout-web` already have background jobs, or should this use a cron
  endpoint, queue worker, or hosted scheduler?
- What database is used in `lookout-web`?
- Should the public ping endpoint live on the app domain or a shorter dedicated
  domain such as `ping.lookout.example`?
- Should monitors belong to users only, or to teams/workspaces?
- Are there plan limits or quotas that should apply at launch?

## Implementation Notes

The current Lookout agent already supports the needed config:

```yaml
heartbeat:
  url: "https://app.lookout.example/ping/{token}"
  interval: 60s
```

No agent change is required for the first version as long as the web app accepts
`GET /ping/{token}` and returns a 2xx response.

If we later want richer behavior, the agent could include version, hostname, and
config metadata in a POST body. That should be a later enhancement, not a
blocker for v1.
