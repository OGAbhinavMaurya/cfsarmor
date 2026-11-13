<div align="center">

# 🛡️ cfsarmor

**Linux CFS bandwidth throttling sentry and quota governor for containerized workloads.**

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.23+-00ADD8?logo=go&logoColor=white)](go.mod)
[![Platform](https://img.shields.io/badge/Platform-Linux%20cgroup%20v1%20%7C%20v2-FCC624?logo=linux&logoColor=black)](#)
[![Kernel](https://img.shields.io/badge/Kernel-5.14%2B%20(Burst%20Support)-lightgrey)](#)

<p align="center">
  <a href="#the-problem">The Problem</a> •
  <a href="#architecture">Architecture</a> •
  <a href="#features">Features</a> •
  <a href="#installation">Installation</a> •
  <a href="#usage">Usage</a> •
  <a href="#how-it-works">How It Works</a> •
  <a href="#license">License</a>
</p>

</div>

---

## The Problem

Containers with CPU limits frequently throttle even when the host node has plenty of idle cores. 

The Linux Completely Fair Scheduler (CFS) enforces quotas over a default 100 millisecond window (`cpu.cfs_period_us` in cgroup v1, `cpu.max` in cgroup v2). Multi-threaded processes (such as Go runtimes with `GOMAXPROCS` or Java thread pools) often burst across available cores and consume an allocated multi-core quota within the first 5 to 10 milliseconds of a period. 

The kernel scheduler then puts all container threads to sleep until the 100 millisecond window resets. This introduces 90 milliseconds or more of artificial latency directly into p99 and p999 response times while average CPU graphs show low utilization.

```
CFS 100ms Enforcement Window:
[===== Active Execution (6.25ms) =====][................ Kernel Throttle Sleep (93.75ms) ................]
                                        ^ Threads put to sleep. Tail latency spikes by ~94ms.

After Proportional Slicing to 10ms:
[= Run (0.6ms) =][... Sleep (9.4ms) ...][= Run (0.6ms) =][... Sleep (9.4ms) ...]
^ Maximum sleep window reduced from 94ms to 9.4ms with identical overall CPU allocation.
```

`cfsarmor` scans cgroups for throttled containers, measures their freeze duration against host Pressure Stall Information (PSI), and recalculates period and quota to shorten kernel sleep windows without requiring pod restarts.

---

## Architecture

```
                  +----------------------------------------------+
                  |         Host Node (/proc/pressure/cpu)        |
                  +----------------------------------------------+
                                         |
                                         v
   +----------------------------------------------------------------------------+
   |                              cfsarmor doctor                               |
   |                                                                            |
   |   1. Scans cgroup v1/v2 paths (/sys/fs/cgroup/kubepods.slice/...)          |
   |   2. Computes throttle ratios (throttled_periods / total_periods)          |
   |   3. Verifies host PSI (flags quota artifact vs real CPU saturation)       |
   |   4. Calculates average freeze duration and estimated p99 tail impact     |
   +----------------------------------------------------------------------------+
                                         |
                                         v
   +----------------------------------------------------------------------------+
   |                               cfsarmor tune                                |
   |                                                                            |
   |   • Proportional Slicing: new_quota = round(old_quota * new_period / old)  |
   |   • Safe Mutation Order: Period written before quota on v1; atomic on v2   |
   |   • Kernel Burst Config: Programs cpu.max.burst / cpu.cfs_burst_us (5.14+)  |
   +----------------------------------------------------------------------------+
```

---

## Comparison

| Approach | P99 Tail Latency | Noisy Neighbor Safety | Granularity | Pod Restarts |
| :--- | :--- | :--- | :--- | :--- |
| **No Limits** | Low | None (runaway pod starves node) | Cluster-wide | Requires restart |
| **Global Kubelet Period (10ms)** | Low | High | Global (10x timer interrupts) | Node reboot |
| **Application automaxprocs** | Medium | High | Per-language (Go only) | App rebuild |
| **cfsarmor (Dynamic Slicing)** | **Low** | **High (Guarded by PSI)** | **Per-cgroup or per-pod** | **Zero restarts** |

---

## Features

- 🔍 **Cgroup v1 & v2 Support**: Scans both unified and legacy cgroup hierarchies automatically.
- ⏱️ **Proportional Period Slicing**: Shortens enforcement periods while mathematically preserving the overall CPU budget.
- 📊 **PSI Verification**: Cross-references `/proc/pressure/cpu` to ensure throttling is an artificial scheduling artifact rather than actual hardware saturation.
- ⚡ **CFS Burst Control**: Configures Linux 5.14+ kernel burst allowances via `cpu.max.burst` and `cpu.cfs_burst_us`.
- 🧪 **Delta Sampling**: `--sample <duration>` isolates current throttling behavior from cumulative historical counters.
- 📦 **Zero Dependencies**: Pure Go standard library implementation with zero external runtime modules.

---

## Installation

Building from source:

```bash
git clone https://github.com/OGAbhinavMaurya/cfsarmor.git
cd cfsarmor
go build -o cfsarmor ./cmd/cfsarmor
sudo install -m 0755 cfsarmor /usr/local/bin/
```

Verify installation:

```bash
cfsarmor --help
```

---

## Usage

### 1. Diagnose active throttling

Scan cgroups to identify containers experiencing CFS scheduling freezes:

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

To isolate current live throttling from historical counters, sample deltas over an interval:

```bash
cfsarmor doctor --sample 2s
```

JSON output for monitoring pipelines and Prometheus scripts:

```bash
cfsarmor doctor --json
```

### 2. Tune period and quota

Reduce the CFS period to shorten maximum freeze windows from 100ms to 10ms:

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

Locate and tune a cgroup by pod name:

```bash
sudo cfsarmor tune --pod checkout-service --period 10ms
```

---

## How It Works

### Proportional period slicing

When modifying `cpu.cfs_period_us` or the period field in `cpu.max`, `cfsarmor` recalculates `quota` to maintain the exact CPU allocation ratio:

```
new_quota = round(old_quota * (new_period / old_period))
```

For example, a container allocated 2 cores has a baseline quota of 200ms per 100ms period. When sliced to a 10ms period, its quota becomes 20ms per 10ms period. Total CPU time allowed per second remains identical (2000ms/sec = 2 cores), but the maximum scheduling stall drops from 100ms to 10ms.

### Mutation safety

On cgroup v2, updates are applied via a single atomic write to `cpu.max`. On cgroup v1, file writes follow strict ordering: when shortening the period, `cpu.cfs_period_us` is written before `cpu.cfs_quota_us` to prevent transient quota exhaustion mid-update.

---

## License

MIT License. See [LICENSE](LICENSE) for details.
