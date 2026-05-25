# Docker Collector Technical Spec

## Purpose

The Docker collector watches Docker container events and turns them into actionable alerts for the person running this monitor.

This project does not have a dashboard. The goal is not to display every Docker event. The goal is to detect important container health problems and alert the individual when something meaningful is happening on their system.

The collector and evaluator should help answer questions like:

- Was a container OOM-killed?
- Did a container crash?
- Did a container stop and fail to come back?
- Did Docker restart a container after it died?
- Is a container repeatedly dying and starting again?
- Is a container unhealthy even though the process is still running?

The key design idea is that Docker health problems are usually not represented by one event. They are usually represented by a short sequence of related events.

Example:

```text
oom
die exitCode=137
stop
start
```

That should be treated as one incident:

```text
Container was OOM-killed and then restarted.
```

It should not become four unrelated alerts.

## Docker Events Model

Docker emits a stream of events from the Docker daemon. Each event says that one action happened to one Docker object.

For this collector, we care mainly about container events.

Example event fields:

```text
Type: container
Action: die
Actor.ID: container ID
Actor.Attributes.name: container name
Actor.Attributes.image: image name
Actor.Attributes.exitCode: exit code, often present on die events
Actor.Attributes.signal: signal, often present on kill events
time/timeNano: event timestamp
```

Important container actions:

```text
create
start
die
stop
restart
kill
oom
destroy
health_status: starting
health_status: healthy
health_status: unhealthy
```

An event is evidence. It is not always the whole story.

For example, a `stop` event only means Docker considers the container stopped. It does not by itself explain why the container stopped.

The reason may be visible in nearby events:

```text
kill signal=15
die exitCode=143
stop
```

or:

```text
oom
die exitCode=137
stop
```

or:

```text
die exitCode=1
stop
```

This is why the evaluator must remember recent events per container.

## Common Event Sequences

### Normal Start

```text
create
start
```

This is not an alert.

### Graceful Stop

```text
kill signal=15
die exitCode=143
stop
```

This commonly happens during `docker stop`, `docker restart`, deploys, and shutdowns.

Do not alert on this sequence by default unless the container was expected to keep running and does not start again within the configured grace period.

### Forced Kill

```text
kill signal=9
die exitCode=137
stop
```

This means the process received `SIGKILL`.

This may be caused by `docker kill`, a stop timeout escalating to `SIGKILL`, or an OOM kill. It should not be classified as confirmed OOM unless an `oom` event was seen or inspect data confirms `OOMKilled=true`.

### OOM Kill

```text
oom
die exitCode=137
stop
```

If restart policy brings the container back:

```text
oom
die exitCode=137
stop
start
```

This should alert immediately. OOM is high signal and usually actionable even if the container restarts.

### Crash And Restart

```text
die exitCode=1
stop
start
```

This means the container process exited with an error and Docker later started it again.

A single crash and restart may be a warning. Repeated crashes are a stronger alert.

### Crash And Stay Down

```text
die exitCode=1
stop
```

If no `start` event arrives within the configured grace period, alert that the container crashed and stayed down.

### Restart Loop

```text
die exitCode=1
start
die exitCode=1
start
die exitCode=1
start
```

This should alert as a restart loop or unstable container.

The important signal is repetition over a short window.

### Healthcheck Failure

```text
health_status: healthy
health_status: unhealthy
```

This means the container process may still be running, but Docker's healthcheck says the app is not healthy.

This should be handled separately from `die` and `stop`.

## Collector Responsibility

The Docker collector should know how to talk to Docker and normalize raw Docker events into this project's internal event shape.

It should:

- Subscribe to Docker container events.
- Filter out event types that are not relevant.
- Convert Docker's event object into an internal `DockerEvent`.
- Include the container ID, timestamp, action, and useful attributes.
- Send the normalized event to the evaluator.

The collector should not decide alert severity.

The collector should not send notifications.

The collector should not try to fully determine whether a container is healthy.

Those decisions belong to the evaluator and alert manager.

## Evaluator Responsibility

The evaluator receives normalized Docker events and decides what they mean.

It should be stateful. It should keep a small amount of recent state per container because related events arrive separately.

Suggested per-container state:

```text
container_id
name
image
last_oom_at
last_die_at
last_start_at
last_stop_at
last_exit_code
last_signal
recent_restart_times
recent_die_times
current_status
current_health_status
pending_down_alert
```

The evaluator uses that state to answer:

- Did this `die` happen shortly after an `oom`?
- Did this `start` happen shortly after a `die`?
- Has this container died several times recently?
- Is this container still down after the grace period?
- Is this event already covered by an existing alert?

## Why State Is Required

State is required for three reasons.

### 1. Group Related Events

Docker may emit several events for one real incident.

```text
oom
die
stop
start
```

Without state, the monitor may send multiple noisy alerts.

With state, the monitor can send one meaningful alert:

```text
Container api was OOM-killed and restarted.
```

### 2. Detect Recovery

This:

```text
die
```

is different from this:

```text
die
start
```

The first may mean the container is still down. The second means the container came back.

The monitor needs to wait briefly before deciding that a container stayed down.

### 3. Detect Repetition

One restart may not require an alert.

Three restarts in five minutes is different.

State lets the monitor count recent events and detect restart loops.

## Initial Alert Rules

These rules are the first useful version of the Docker monitor.

The exact thresholds should be configurable later, but hardcoded constants are acceptable for the first implementation.

Suggested defaults:

```text
down_grace_period: 30 seconds
related_event_window: 10 seconds
restart_loop_window: 5 minutes
restart_loop_threshold: 3 restarts
```

### Alert: Container OOM-Killed

Trigger:

```text
event.Action == "oom"
```

Behavior:

- Alert immediately.
- Store `last_oom_at` for the container.
- Do not send a separate crash alert if a `die exitCode=137` follows shortly after.

Message shape:

```text
Container api was OOM-killed.
```

If a later `start` event arrives shortly after:

```text
Container api was OOM-killed and restarted after 6 seconds.
```

This update can be implemented later. The first implementation can simply alert on OOM immediately.

### Alert: Container Crashed And Stayed Down

Trigger:

```text
event.Action == "die"
exitCode != 0
no recent OOM event for the same container
no start event for the same container within down_grace_period
```

Behavior:

- Do not alert immediately on `die`.
- Record the die event.
- Start a pending down check.
- If no `start` arrives within the grace period, alert.
- If `start` arrives before the grace period expires, mark as recovered and do not send a down alert.

Message shape:

```text
Container api crashed with exit code 1 and has not restarted after 30 seconds.
```

### Alert: Container Restart Loop

Trigger:

```text
container has 3 or more restart cycles within 5 minutes
```

A restart cycle can be detected when:

```text
start event arrives after a recent die event
```

Behavior:

- Record restart timestamps.
- Remove timestamps older than `restart_loop_window`.
- Alert when count reaches `restart_loop_threshold`.
- Avoid sending this alert repeatedly for every subsequent restart. Use a cooldown later.

Message shape:

```text
Container api restarted 3 times in 5 minutes.
```

### Alert: Container Unhealthy

Trigger:

```text
event.Action == "health_status: unhealthy"
```

Initial behavior:

- Record health status.
- Alert if the container remains unhealthy for a grace period.

Suggested future threshold:

```text
unhealthy_grace_period: 60 seconds
```

Message shape:

```text
Container api is unhealthy.
```

This can be implemented after OOM, crash, and restart-loop detection.

## Events That Should Usually Not Alert Alone

These events are useful context, but should not usually alert by themselves:

```text
start
stop
restart
kill
die exitCode=0
```

Reasons:

- `start` often means recovery or normal startup.
- `stop` does not explain why the container stopped.
- `restart` may be manual or expected.
- `kill` may happen during graceful stop.
- `die exitCode=0` can be normal for short-lived jobs.

They become important when combined with nearby events or repeated over time.

## Event Handling Sketch

This is not final code. It describes the behavior.

```text
on docker event:
  state = state_by_container_id[event.container_id]
  update name/image from event attributes

  if event.action == "oom":
    state.last_oom_at = event.timestamp
    alert "container oom-killed"
    return

  if event.action == "die":
    state.last_die_at = event.timestamp
    state.last_exit_code = event.exit_code
    append event.timestamp to state.recent_die_times

    if event happened soon after state.last_oom_at:
      mark die as part of oom incident
      return

    if event.exit_code != 0:
      create pending down check for event.timestamp + down_grace_period
      return

    return

  if event.action == "start":
    state.last_start_at = event.timestamp

    if state.last_die_at is recent:
      append event.timestamp to state.recent_restart_times
      clear pending down check

    remove old restart timestamps

    if restart count >= restart_loop_threshold:
      alert "container restart loop"

    return

  if event.action == "stop":
    state.last_stop_at = event.timestamp
    return

periodically:
  for each pending down check:
    if deadline passed and no later start event:
      alert "container crashed and stayed down"
```

## Alert Deduplication

The monitor should avoid alert storms.

For the first version:

- OOM alerts can be immediate.
- Crash-down alerts should wait for the grace period.
- Restart-loop alerts should have a cooldown.

Suggested future cooldown:

```text
same alert type + same container: do not repeat for 10 minutes
```

Example:

If a container restarts 10 times in 5 minutes, send:

```text
Container api restarted 3 times in 5 minutes.
```

Do not send a new alert for restart 4, 5, 6, and so on unless the cooldown expires or the incident resolves and happens again.

## Stream Reliability

Docker events are a live stream. Streams can disconnect.

If the collector disconnects and reconnects, events may be missed during the gap.

The first implementation can reconnect and continue.

Later, the collector should:

- Reconnect with a `since` timestamp.
- Periodically inspect current containers to reconcile actual state.
- On startup, inspect existing containers before listening to new events.

This matters because the event stream is good for fast detection, but current Docker state is the source of truth after gaps.

## Testing Scenarios

Use local containers to test event behavior.

### Start And Stop

```text
docker run -d --name monitor-probe nginx
docker stop monitor-probe
docker start monitor-probe
docker rm -f monitor-probe
```

Expected:

- Events are observed.
- No OOM alert.
- No restart-loop alert.
- A stop alone should not immediately alert.

### Crash

```text
docker run --name crash-probe alpine sh -c "exit 1"
```

Expected:

- `die` event with non-zero exit code.
- If no start follows within the grace period, alert crashed-and-stayed-down.

### Restart Policy Crash Loop

```text
docker run -d --name loop-probe --restart=always alpine sh -c "exit 1"
```

Expected:

- Repeated die/start behavior.
- Restart-loop alert after threshold is reached.

Cleanup:

```text
docker rm -f loop-probe
```

### OOM

Exact behavior depends on host Docker settings and available memory.

Example shape:

```text
docker run --name oom-probe --memory=20m alpine sh -c 'x="x"; while true; do x="$x$x$x$x"; done'
```

Expected:

- `oom` event.
- Usually followed by `die exitCode=137`.
- Alert OOM immediately.

Cleanup:

```text
docker rm -f oom-probe
```

## Implementation Order

Recommended order:

1. Forward relevant Docker container events from the collector:
   `oom`, `die`, `start`, `stop`, `restart`, and later health events.
2. Add evaluator state keyed by container ID.
3. Implement immediate OOM alerting.
4. Implement pending crash-and-stayed-down alerting.
5. Implement restart-loop detection.
6. Add alert deduplication/cooldown.
7. Add healthcheck event handling.
8. Add event stream reconnect and container state reconciliation.

The first useful milestone is:

```text
Alert on OOM.
Alert when a non-zero die does not recover within 30 seconds.
Alert when a container restarts 3 times in 5 minutes.
```

That is enough to make the Docker collector useful without trying to handle every Docker edge case at once.
