package cgroup

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ReadV2 inspects a cgroup v2 directory and returns its configuration and statistics.
func ReadV2(dir string) (*Cgroup, error) {
	maxPath := filepath.Join(dir, "cpu.max")
	maxBytes, err := os.ReadFile(maxPath)
	if err != nil {
		return nil, fmt.Errorf("reading v2 cpu.max: %w", err)
	}

	quota, period, err := ParseV2CPUMax(string(maxBytes))
	if err != nil {
		return nil, fmt.Errorf("parsing v2 cpu.max: %w", err)
	}

	var burst int64
	burstPath := filepath.Join(dir, "cpu.max.burst")
	if burstBytes, err := os.ReadFile(burstPath); err == nil {
		burstStr := strings.TrimSpace(string(burstBytes))
		if burstStr != "max" {
			if b, err := strconv.ParseInt(burstStr, 10, 64); err == nil {
				burst = b
			}
		}
	}

	stat, err := ReadV2Stat(dir)
	if err != nil {
		return nil, err
	}

	return &Cgroup{
		Path:       dir,
		Name:       filepath.Base(dir),
		Version:    V2,
		QuotaUsec:  quota,
		PeriodUsec: period,
		BurstUsec:  burst,
		Stat:       stat,
	}, nil
}

// ParseV2CPUMax parses the content of a cpu.max file.
// Format is "<quota> <period>" where quota can be a number or "max".
func ParseV2CPUMax(content string) (quota int64, period int64, err error) {
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return 0, 0, fmt.Errorf("empty cpu.max content")
	}

	if fields[0] == "max" {
		quota = -1
	} else {
		quota, err = strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid quota %q: %w", fields[0], err)
		}
	}

	if len(fields) > 1 {
		period, err = strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid period %q: %w", fields[1], err)
		}
	} else {
		period = 100000 // Default CFS period
	}

	return quota, period, nil
}

// ReadV2Stat parses cpu.stat in cgroup v2 where throttled_usec is in microseconds.
func ReadV2Stat(dir string) (CPUStat, error) {
	statPath := filepath.Join(dir, "cpu.stat")
	f, err := os.Open(statPath)
	if err != nil {
		return CPUStat{}, fmt.Errorf("opening v2 cpu.stat: %w", err)
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
		case "throttled_usec":
			stat.ThrottledUsec = val
		case "nr_bursts":
			stat.NrBursts = val
		case "burst_usec":
			stat.BurstUsec = val
		}
	}

	if err := scanner.Err(); err != nil {
		return stat, fmt.Errorf("scanning v2 cpu.stat: %w", err)
	}

	return stat, nil
}

// WriteV2 updates quota and period via cpu.max and optional burst via cpu.max.burst.
func WriteV2(dir string, quotaUsec, periodUsec, burstUsec int64) error {
	maxPath := filepath.Join(dir, "cpu.max")
	var quotaStr string
	if quotaUsec == -1 {
		quotaStr = "max"
	} else {
		quotaStr = strconv.FormatInt(quotaUsec, 10)
	}

	content := fmt.Sprintf("%s %d\n", quotaStr, periodUsec)
	if err := os.WriteFile(maxPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing v2 cpu.max: %w", err)
	}

	if burstUsec >= 0 {
		burstPath := filepath.Join(dir, "cpu.max.burst")
		if _, err := os.Stat(burstPath); err == nil {
			burstContent := strconv.FormatInt(burstUsec, 10) + "\n"
			if err := os.WriteFile(burstPath, []byte(burstContent), 0644); err != nil {
				return fmt.Errorf("writing v2 cpu.max.burst: %w", err)
			}
		}
	}

	return nil
}
