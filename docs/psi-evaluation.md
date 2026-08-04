# PSI disagreement evaluation

This experiment tests the claim that utilization and resource pressure can
disagree, and that PSI more closely follows lost workload productivity in those
cases. Do not treat the expected outcomes below as results: record and report
what the experiment actually produces.

## Requirements

- A disposable Linux host or VM with `/proc/pressure/cpu`, `memory`, and `io`
- cgroup v2 and systemd
- `stress-ng`, `curl`, and a build of Lookout containing the PSI collector
- Enough host memory that a 1 GiB test workload does not make the host itself
  exceed Lookout's memory-utilization threshold

Do not run these workloads on a production server. They intentionally saturate
CPU or force aggressive memory reclaim.

## Lookout configuration

Use a five-second collection interval so alert timing and short-term pressure
are visible. Keep both the existing utilization alerts and the PSI alerts
enabled so the same run evaluates both designs.

```yaml
collection_interval: 5s

metrics:
  enabled: true
  listen: "127.0.0.1:9100"

alerts:
  stale_after: 30s
  cpu:
    threshold: 85
    resolve_below: 80
    for: 30s
    severity: warning
  memory:
    threshold: 85
    resolve_below: 80
    for: 30s
    severity: warning
  pressure:
    enabled: true
    cpu:
      threshold: 20
      resolve_below: 15
      for: 30s
      severity: warning
    memory:
      threshold: 5
      resolve_below: 3
      for: 30s
      severity: critical
    io:
      threshold: 10
      resolve_below: 5
      for: 30s
      severity: warning
```

Capture the relevant metrics throughout every run:

```sh
while sleep 5; do
  date --iso-8601=seconds
  curl -fsS http://127.0.0.1:9100/metrics |
    grep -E '^(cpu_used_percent|memory_used_percent|pressure_(cpu|memory))'
done | tee lookout-psi-run.log
```

Capture alerts separately with `journalctl -fu lookout`. Record the workload's
`stress-ng --metrics-brief` output as a basic throughput measurement. If a real
service benchmark is available, its throughput and p95/p99 latency are better
ground truth.

## Case 1: high utilization without CPU pressure

First run a quiet baseline, then run one CPU worker per available CPU for at
least 90 seconds:

```sh
cpu_count=$(nproc)
stress-ng --cpu "$cpu_count" --timeout 90s --metrics-brief
```

The proposed disagreement is:

- `cpu_used_percent` stays above 85% and the existing CPU alert fires.
- `pressure_cpu_some_stall_percent` remains below its threshold because there
  is approximately one runnable worker per CPU.
- Aggregate workload throughput remains stable rather than collapsing from
  scheduler contention.

As a positive control, repeat with twice as many workers. CPU utilization will
look similar, but CPU `some` pressure should rise because runnable work is now
waiting for CPU time.

```sh
stress-ng --cpu "$((cpu_count * 2))" --timeout 90s --metrics-brief
```

## Case 2: memory pressure while host availability looks healthy

Run the workload in a transient cgroup whose `MemoryHigh` is below its working
set. `MemoryMax` is intentionally higher to create reclaim pressure without
making an OOM kill the intended outcome.

```sh
sudo systemd-run \
  --unit=lookout-psi-memory-test \
  --property=MemoryHigh=512M \
  --property=MemoryMax=2G \
  stress-ng --vm 1 --vm-bytes 1G --vm-keep --timeout 90s --metrics-brief
```

While it is running, inspect the workload-local signal as a cross-check:

```sh
control_group=$(systemctl show lookout-psi-memory-test.service --property=ControlGroup --value)
cat "/sys/fs/cgroup${control_group}/memory.pressure"
```

The proposed disagreement is:

- Host `memory_used_percent` remains below 85%, so the existing memory alert
  does not fire.
- System-wide `pressure_memory_full_stall_percent` rises and the PSI alert
  fires while workload throughput falls or latency rises.
- The cgroup's `memory.pressure` confirms that the constrained workload is the
  source of the stalls.

System-wide `full` pressure can be diluted when unrelated tasks continue making
progress. If cgroup `full` is high but `/proc/pressure/memory` is not, report
that result rather than increasing the workload until it fits the hypothesis;
it is evidence that a future per-cgroup PSI collector may be necessary.

## Reporting

Run the baseline and each treatment at least five times. Report median and
spread for utilization, pressure, throughput/latency, and alert detection time.
A useful result table has one row per run and explicitly records whether each
Lookout alert fired. This makes false positives, false negatives, and PSI's own
limitations visible instead of relying on a single illustrative trace.
