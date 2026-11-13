package sentry

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/OGAbhinavMaurya/cfsarmor/pkg/cgroup"
)

// DiagnosisLevel represents the severity of CFS throttling.
type DiagnosisLevel string

const (
	LevelHealthy   DiagnosisLevel = "HEALTHY"
	LevelWarning   DiagnosisLevel = "WARNING"
	LevelThrottled DiagnosisLevel = "THROTTLED"
)

// Diagnosis encapsulates the throttling assessment and remediation plan for a cgroup.
type Diagnosis struct {
	CgroupPath       string         `json:"cgroup_path"`
	ContainerName    string         `json:"container_name"`
	Level            DiagnosisLevel `json:"level"`
	ThrottleRatio    float64        `json:"throttle_ratio_pct"`
	TotalPeriods     uint64         `json:"total_periods"`
	ThrottledCount   uint64         `json:"throttled_count"`
	ThrottledMs      float64        `json:"throttled_ms"`
	AvgFreezeMs      float64        `json:"avg_freeze_ms"`
	EstTailPenaltyMs float64        `json:"est_tail_penalty_ms"`
	PeriodUsec       int64          `json:"period_usec"`
	QuotaUsec        int64          `json:"quota_usec"`
	BurstUsec        int64          `json:"burst_usec"`
	PSIHeadroom      string         `json:"psi_headroom,omitempty"`
	Recommendation   string         `json:"recommendation,omitempty"`
}

// Diagnose evaluates a cgroup and produces a latency and throttling assessment.
func Diagnose(cg *cgroup.Cgroup, thresholdPct float64, psi *PSIStatus) *Diagnosis {
	diag := &Diagnosis{
		CgroupPath:    cg.Path,
		ContainerName: ExtractIdentifier(cg.Path),
		PeriodUsec:    cg.PeriodUsec,
		QuotaUsec:     cg.QuotaUsec,
		BurstUsec:     cg.BurstUsec,
	}

	totalPeriods := cg.Stat.NrPeriods
	throttledCount := cg.Stat.NrThrottled
	throttledUsec := cg.Stat.ThrottledUsec

	diag.TotalPeriods = totalPeriods
	diag.ThrottledCount = throttledCount
	diag.ThrottledMs = float64(throttledUsec) / 1000.0

	if totalPeriods > 0 {
		diag.ThrottleRatio = (float64(throttledCount) / float64(totalPeriods)) * 100.0
	}

	if throttledCount > 0 {
		diag.AvgFreezeMs = float64(throttledUsec) / float64(throttledCount) / 1000.0
		diag.EstTailPenaltyMs = diag.AvgFreezeMs
	}

	if diag.ThrottleRatio >= thresholdPct && throttledCount > 0 {
		diag.Level = LevelThrottled
	} else if throttledCount > 0 {
		diag.Level = LevelWarning
	} else {
		diag.Level = LevelHealthy
	}

	if psi != nil {
		if psi.Some.Avg10 > 10.0 {
			diag.PSIHeadroom = fmt.Sprintf("Host CPU pressure is elevated (some avg10: %.1f%%). Throttling may be driven by host contention.", psi.Some.Avg10)
		} else {
			diag.PSIHeadroom = fmt.Sprintf("Host CPU pressure is low (some avg10: %.1f%%). Throttling is an artificial CFS period artifact.", psi.Some.Avg10)
		}
	}

	if diag.Level == LevelThrottled || diag.Level == LevelWarning {
		if cg.QuotaUsec > 0 {
			targetPeriod := int64(10000) // 10ms
			if cg.PeriodUsec <= 20000 {
				targetPeriod = 5000 // 5ms
			}
			diag.Recommendation = fmt.Sprintf("Tune period to %s: 'cfsarmor tune --cgroup %s --period %s'",
				cgroup.FormatDurationUsec(targetPeriod), cg.Path, cgroup.FormatDurationUsec(targetPeriod))
		} else {
			diag.Recommendation = "Quota is unlimited; check parent slice limits."
		}
	}

	return diag
}

// DiagnoseDelta samples cgroups over a measurement duration to isolate current throttling from historical counters.
func DiagnoseDelta(cgroups []*cgroup.Cgroup, sampleDuration time.Duration, thresholdPct float64, psi *PSIStatus) ([]*Diagnosis, error) {
	if sampleDuration <= 0 {
		var results []*Diagnosis
		for _, cg := range cgroups {
			results = append(results, Diagnose(cg, thresholdPct, psi))
		}
		return results, nil
	}

	// Capture baseline stats
	type snapshot struct {
		cg   *cgroup.Cgroup
		stat cgroup.CPUStat
	}
	baselines := make([]snapshot, 0, len(cgroups))
	for _, cg := range cgroups {
		baselines = append(baselines, snapshot{cg: cg, stat: cg.Stat})
	}

	time.Sleep(sampleDuration)

	var results []*Diagnosis
	for _, b := range baselines {
		var updatedStat cgroup.CPUStat
		var err error
		if b.cg.Version == cgroup.V2 {
			updatedStat, err = cgroup.ReadV2Stat(b.cg.Path)
		} else {
			updatedStat, err = cgroup.ReadV1Stat(b.cg.Path)
		}
		if err != nil {
			continue
		}

		deltaPeriods := uint64(0)
		if updatedStat.NrPeriods >= b.stat.NrPeriods {
			deltaPeriods = updatedStat.NrPeriods - b.stat.NrPeriods
		}

		deltaThrottled := uint64(0)
		if updatedStat.NrThrottled >= b.stat.NrThrottled {
			deltaThrottled = updatedStat.NrThrottled - b.stat.NrThrottled
		}

		deltaThrottledUsec := uint64(0)
		if updatedStat.ThrottledUsec >= b.stat.ThrottledUsec {
			deltaThrottledUsec = updatedStat.ThrottledUsec - b.stat.ThrottledUsec
		}

		virtualCG := *b.cg
		virtualCG.Stat = cgroup.CPUStat{
			NrPeriods:     deltaPeriods,
			NrThrottled:   deltaThrottled,
			ThrottledUsec: deltaThrottledUsec,
		}

		results = append(results, Diagnose(&virtualCG, thresholdPct, psi))
	}

	return results, nil
}

// ExtractIdentifier derives a clean container or pod name from the cgroup path.
func ExtractIdentifier(path string) string {
	base := filepath.Base(path)
	// Check for kubepods pod slice notation
	if strings.Contains(path, "kubepods") {
		parts := strings.Split(path, "/")
		for i := len(parts) - 1; i >= 0; i-- {
			if strings.HasPrefix(parts[i], "pod") || strings.HasPrefix(parts[i], "crio-") || strings.HasPrefix(parts[i], "docker-") {
				return parts[i]
			}
		}
	}
	return base
}
