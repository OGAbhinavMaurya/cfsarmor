# cfsarmor

Linux CFS bandwidth throttling sentry and quota tuner for containerized workloads.

Containers with CPU limits frequently throttle even when the host has idle cores. The Linux Completely Fair Scheduler (CFS) enforces quotas over a default 100 millisecond window (`cpu.cfs_period_us` in cgroup v1, `cpu.max` in cgroup v2). Multi-threaded processes (such as Go runtimes with GOMAXPROCS or Java thread pools) often burst across available cores and burn through a multi-core quota in the first 5 to 10 milliseconds of a period. The kernel scheduler then puts those threads to sleep until the 100 millisecond window resets, adding 90 milliseconds or more directly to request tail latency.

`cfsarmor` scans cgroups for throttled containers, measures their freeze duration against host Pressure Stall Information (PSI), and recalculates period and quota to shorten kernel sleep windows without requiring pod restarts.

## Features

- Scans cgroup v1 and cgroup v2 hierarchies.
- Calculates throttle ratio, average freeze duration per throttled period, and estimated p99 latency impact.
- Reads `/proc/pressure/cpu` to verify whether throttling is a quota artifact or caused by actual node CPU saturation.
- Scales quota proportionally when shortening period (`new_quota = old_quota * new_period / old_period`).
- Configures kernel 5.14+ CFS burst allowance via `cpu.max.burst` and `cpu.cfs_burst_us`.
- Supports delta sampling over an interval to isolate active throttling from historical totals.
- Single static Go binary with no external dependencies.

## Installation

```bash
git clone https://github.com/OGAbhinavMaurya/cfsarmor.git
cd cfsarmor
go build -o cfsarmor ./cmd/cfsarmor
sudo install -m 0755 cfsarmor /usr/local/bin/
```

## Usage

### Diagnose throttling

Scan active cgroups for containers experiencing scheduler throttling:

```bash
cfsarmor doctor
```

Example output:

```text
CFS Throttling Report (scanned 42 cgroups):
Host CPU Pressure: some avg10=0.20%, avg60=0.15%

[THROTTLED] pod-checkout-service-7f89d
  Path:            /sys/fs/cgroup/kubepods.slice/pod-checkout-service-7f89d
  Throttled:       34.2% of periods (3420 / 10000 periods)
  Average Freeze:  68.4 ms per throttled period
  P99 Penalty:     +68.4 ms estimated tail latency impact
  Period / Quota:  100ms / 200ms
  Context:         Host CPU pressure is low (some avg10: 0.2%). Throttling is an artificial CFS period artifact.
  Remediation:     Tune period to 10ms: 'cfsarmor tune --cgroup /sys/fs/cgroup/kubepods.slice/pod-checkout-service-7f89d --period 10ms'
```

To isolate active throttling from cumulative historical counters, sample deltas over an interval:

```bash
cfsarmor doctor --sample 2s
```

JSON output for metrics collection and automation scripts:

```bash
cfsarmor doctor --json
```

### Tune period and quota

Shorten the CFS period to reduce maximum freeze duration from 100ms to 10ms:

```bash
sudo cfsarmor tune --cgroup /sys/fs/cgroup/kubepods.slice/pod-checkout-service-7f89d --period 10ms
```

Preview changes without writing to sysfs:

```bash
cfsarmor tune --cgroup /sys/fs/cgroup/kubepods.slice/pod-checkout-service-7f89d --period 10ms --dry-run
```

Configure burst headroom on Linux 5.14+ kernels:

```bash
sudo cfsarmor tune --cgroup /sys/fs/cgroup/kubepods.slice/pod-checkout-service-7f89d --period 10ms --burst 50%
```

Locate a cgroup by pod name:

```bash
sudo cfsarmor tune --pod checkout-service --period 10ms
```

## How it works

### Proportional period slicing

When altering `cpu.cfs_period_us` or the period field in `cpu.max`, `cfsarmor` recalculates `quota` to preserve the CPU allocation ratio:

```
new_quota = round(old_quota * (new_period / old_period))
```

For instance, a container allocated 2 cores has a baseline quota of 200ms per 100ms period. When sliced to a 10ms period, its quota becomes 20ms per 10ms period. The total CPU time allowed per second remains identical (2000ms/sec = 2 cores), but the maximum scheduling stall drops from 100ms to 10ms.

### Mutation safety

On cgroup v2, updates are applied via a single atomic write to `cpu.max`. On cgroup v1, file writes follow order of operations: when shortening the period, `cpu.cfs_period_us` is written before `cpu.cfs_quota_us` to prevent transient quota exhaustion mid-update.

## License

MIT
