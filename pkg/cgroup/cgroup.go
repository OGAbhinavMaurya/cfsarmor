package cgroup

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Version represents the cgroup subsystem hierarchy version.
type Version int

const (
	// Unknown indicates an unrecognized cgroup layout.
	Unknown Version = 0
	// V1 represents the legacy cgroup v1 multi-hierarchy layout.
	V1 Version = 1
	// V2 represents the unified cgroup v2 hierarchy.
	V2 Version = 2
)

func (v Version) String() string {
	switch v {
	case V1:
		return "v1"
	case V2:
		return "v2"
	default:
		return "unknown"
	}
}

// CPUStat holds scheduler bandwidth statistics from cpu.stat.
type CPUStat struct {
	NrPeriods     uint64 `json:"nr_periods"`
	NrThrottled   uint64 `json:"nr_throttled"`
	ThrottledUsec uint64 `json:"throttled_usec"` // Normalized to microseconds across v1 and v2
	NrBursts      uint64 `json:"nr_bursts"`
	BurstUsec     uint64 `json:"burst_usec"`
}

// Cgroup represents an active cgroup node on the system.
type Cgroup struct {
	Path       string  `json:"path"`
	Name       string  `json:"name"`
	Version    Version `json:"version"`
	QuotaUsec  int64   `json:"quota_usec"`  // -1 if unlimited ("max")
	PeriodUsec int64   `json:"period_usec"` // CFS period in microseconds (e.g. 100000)
	BurstUsec  int64   `json:"burst_usec"`  // CFS burst in microseconds (0 if unsupported/disabled)
	Stat       CPUStat `json:"stat"`
}

// DetectVersion checks the cgroup version of a root path or system mount.
func DetectVersion(path string) Version {
	if _, err := os.Stat(filepath.Join(path, "cgroup.controllers")); err == nil {
		return V2
	}
	if _, err := os.Stat(filepath.Join(path, "cpu.max")); err == nil {
		return V2
	}
	if _, err := os.Stat(filepath.Join(path, "cpu.cfs_period_us")); err == nil {
		return V1
	}
	if _, err := os.Stat(filepath.Join(path, "cpu", "cpu.cfs_period_us")); err == nil {
		return V1
	}
	return Unknown
}

// ScaleQuota calculates the new quota when modifying the CFS period,
// preserving the exact CPU allocation ratio.
// Returns -1 without error if oldQuota is unlimited (-1).
func ScaleQuota(oldQuota, oldPeriod, newPeriod int64) (int64, error) {
	if oldPeriod <= 0 {
		return 0, errors.New("old period must be greater than zero")
	}
	if newPeriod <= 0 {
		return 0, errors.New("new period must be greater than zero")
	}
	if newPeriod < 1000 {
		return 0, fmt.Errorf("new period %d us is below kernel minimum 1000 us (1ms)", newPeriod)
	}
	if oldQuota == -1 {
		return -1, nil
	}
	if oldQuota <= 0 {
		return 0, errors.New("old quota must be -1 (unlimited) or greater than zero")
	}

	ratio := float64(oldQuota) / float64(oldPeriod)
	newQuota := int64(math.Round(ratio * float64(newPeriod)))

	if newQuota < 1000 {
		return 0, fmt.Errorf("scaled quota %d us is below kernel minimum 1000 us (1ms)", newQuota)
	}

	return newQuota, nil
}

// ParseDurationUsec parses strings like "10ms", "50000us", "100000", "0.01s" into microseconds.
func ParseDurationUsec(val string) (int64, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return 0, errors.New("empty duration string")
	}

	// Direct integer microsecond input
	if n, err := strconv.ParseInt(val, 10, 64); err == nil {
		return n, nil
	}

	// Microsecond suffix
	if strings.HasSuffix(val, "us") || strings.HasSuffix(val, "µs") {
		numStr := strings.TrimRight(val, "uµs")
		return strconv.ParseInt(numStr, 10, 64)
	}

	// Standard Go duration (ms, s)
	d, err := time.ParseDuration(val)
	if err != nil {
		return 0, fmt.Errorf("invalid duration format %q: %w", val, err)
	}

	return d.Microseconds(), nil
}

// FormatDurationUsec formats microseconds into a readable unit (e.g. 10ms, 100ms, 1.5s).
func FormatDurationUsec(usec int64) string {
	if usec < 0 {
		return "unlimited"
	}
	if usec%1000 == 0 {
		ms := usec / 1000
		if ms%1000 == 0 && ms >= 1000 {
			return fmt.Sprintf("%ds", ms/1000)
		}
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%dµs", usec)
}

// FindCgroups scans a given root directory for cgroups with CPU bandwidth controls.
func FindCgroups(root string) ([]*Cgroup, error) {
	var cgroups []*Cgroup

	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil // Skip unreadable directories
		}
		if !info.IsDir() {
			return nil
		}

		// Check for cgroup v2
		if _, err := os.Stat(filepath.Join(path, "cpu.max")); err == nil {
			cg, err := ReadV2(path)
			if err == nil {
				cgroups = append(cgroups, cg)
			}
			return nil
		}

		// Check for cgroup v1
		if _, err := os.Stat(filepath.Join(path, "cpu.cfs_period_us")); err == nil {
			cg, err := ReadV1(path)
			if err == nil {
				cgroups = append(cgroups, cg)
			}
			return nil
		}

		return nil
	})

	if err != nil {
		return nil, err
	}
	return cgroups, nil
}
