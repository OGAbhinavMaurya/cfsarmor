package cgroup

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ReadV1 inspects a cgroup v1 directory and returns its configuration and statistics.
func ReadV1(dir string) (*Cgroup, error) {
	periodPath := filepath.Join(dir, "cpu.cfs_period_us")
	quotaPath := filepath.Join(dir, "cpu.cfs_quota_us")

	periodBytes, err := os.ReadFile(periodPath)
	if err != nil {
		return nil, fmt.Errorf("reading v1 cpu.cfs_period_us: %w", err)
	}
	period, err := strconv.ParseInt(strings.TrimSpace(string(periodBytes)), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parsing v1 cpu.cfs_period_us: %w", err)
	}

	quotaBytes, err := os.ReadFile(quotaPath)
	if err != nil {
		return nil, fmt.Errorf("reading v1 cpu.cfs_quota_us: %w", err)
	}
	quota, err := strconv.ParseInt(strings.TrimSpace(string(quotaBytes)), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parsing v1 cpu.cfs_quota_us: %w", err)
	}

	var burst int64
	burstPath := filepath.Join(dir, "cpu.cfs_burst_us")
	if burstBytes, err := os.ReadFile(burstPath); err == nil {
		if b, err := strconv.ParseInt(strings.TrimSpace(string(burstBytes)), 10, 64); err == nil {
			burst = b
		}
	}

	stat, err := ReadV1Stat(dir)
	if err != nil {
		return nil, err
	}

	return &Cgroup{
		Path:       dir,
		Name:       filepath.Base(dir),
		Version:    V1,
		QuotaUsec:  quota,
		PeriodUsec: period,
		BurstUsec:  burst,
		Stat:       stat,
	}, nil
}

// ReadV1Stat parses cpu.stat in cgroup v1 and normalizes throttled_time (ns) to microseconds.
func ReadV1Stat(dir string) (CPUStat, error) {
	statPath := filepath.Join(dir, "cpu.stat")
	f, err := os.Open(statPath)
	if err != nil {
		return CPUStat{}, fmt.Errorf("opening v1 cpu.stat: %w", err)
	}
	defer f.Close()

	var stat CPUStat
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		key := fields[0]
		val, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}

		switch key {
		case "nr_periods":
			stat.NrPeriods = val
		case "nr_throttled":
			stat.NrThrottled = val
		case "throttled_time":
			// In cgroup v1, throttled_time is reported in nanoseconds.
			// Normalize to microseconds.
			stat.ThrottledUsec = val / 1000
		case "nr_bursts":
			stat.NrBursts = val
		case "burst_time":
			stat.BurstUsec = val / 1000
		}
	}

	if err := scanner.Err(); err != nil {
		return stat, fmt.Errorf("scanning v1 cpu.stat: %w", err)
	}

	return stat, nil
}

// WriteV1 writes quota, period, and burst parameters to cgroup v1 files.
// Order of writing is sequenced to avoid transient throttling:
// when decreasing period, period is written first; when increasing, quota is written first.
func WriteV1(dir string, quotaUsec, periodUsec, burstUsec int64) error {
	periodPath := filepath.Join(dir, "cpu.cfs_period_us")
	quotaPath := filepath.Join(dir, "cpu.cfs_quota_us")
	burstPath := filepath.Join(dir, "cpu.cfs_burst_us")

	// Read current period to determine safe write sequence
	var currentPeriod int64 = 100000
	if curBytes, err := os.ReadFile(periodPath); err == nil {
		if cp, err := strconv.ParseInt(strings.TrimSpace(string(curBytes)), 10, 64); err == nil && cp > 0 {
			currentPeriod = cp
		}
	}

	writePeriod := func() error {
		return os.WriteFile(periodPath, []byte(strconv.FormatInt(periodUsec, 10)+"\n"), 0644)
	}
	writeQuota := func() error {
		return os.WriteFile(quotaPath, []byte(strconv.FormatInt(quotaUsec, 10)+"\n"), 0644)
	}

	if periodUsec < currentPeriod {
		// Shrinking period: expand relative quota headroom first by updating period
		if err := writePeriod(); err != nil {
			return fmt.Errorf("writing cpu.cfs_period_us: %w", err)
		}
		if err := writeQuota(); err != nil {
			return fmt.Errorf("writing cpu.cfs_quota_us: %w", err)
		}
	} else {
		// Expanding period: update quota first
		if err := writeQuota(); err != nil {
			return fmt.Errorf("writing cpu.cfs_quota_us: %w", err)
		}
		if err := writePeriod(); err != nil {
			return fmt.Errorf("writing cpu.cfs_period_us: %w", err)
		}
	}

	// Update burst if supported and requested (burstUsec >= 0)
	if burstUsec >= 0 {
		if _, err := os.Stat(burstPath); err == nil {
			if err := os.WriteFile(burstPath, []byte(strconv.FormatInt(burstUsec, 10)+"\n"), 0644); err != nil {
				return fmt.Errorf("writing cpu.cfs_burst_us: %w", err)
			}
		}
	}

	return nil
}
