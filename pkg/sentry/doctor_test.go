package sentry

import (
	"strings"
	"testing"

	"github.com/OGAbhinavMaurya/cfsarmor/pkg/cgroup"
)

func TestDiagnose_Healthy(t *testing.T) {
	cg := &cgroup.Cgroup{
		Path:       "/sys/fs/cgroup/kubepods.slice/pod1",
		Version:    cgroup.V2,
		QuotaUsec:  200000,
		PeriodUsec: 100000,
		Stat: cgroup.CPUStat{
			NrPeriods:     500,
			NrThrottled:   0,
			ThrottledUsec: 0,
		},
	}

	diag := Diagnose(cg, 5.0, nil)
	if diag.Level != LevelHealthy {
		t.Errorf("expected LevelHealthy, got %s", diag.Level)
	}
	if diag.ThrottleRatio != 0.0 {
		t.Errorf("expected 0.0%% throttle ratio, got %f", diag.ThrottleRatio)
	}
	if diag.AvgFreezeMs != 0.0 {
		t.Errorf("expected 0.0ms freeze, got %f", diag.AvgFreezeMs)
	}
}

func TestDiagnose_Throttled(t *testing.T) {
	cg := &cgroup.Cgroup{
		Path:       "/sys/fs/cgroup/kubepods.slice/pod-test-123",
		Version:    cgroup.V2,
		QuotaUsec:  200000,
		PeriodUsec: 100000,
		Stat: cgroup.CPUStat{
			NrPeriods:     100,
			NrThrottled:   40,
			ThrottledUsec: 2400000, // 2.4s total, 60ms avg freeze per throttled period
		},
	}

	psi := &PSIStatus{
		Some: PSIMetric{Avg10: 1.2},
	}

	diag := Diagnose(cg, 5.0, psi)
	if diag.Level != LevelThrottled {
		t.Errorf("expected LevelThrottled, got %s", diag.Level)
	}
	if diag.ThrottleRatio != 40.0 {
		t.Errorf("expected 40.0%% throttle ratio, got %f", diag.ThrottleRatio)
	}
	if diag.AvgFreezeMs != 60.0 {
		t.Errorf("expected 60.0ms freeze, got %f", diag.AvgFreezeMs)
	}
	if diag.EstTailPenaltyMs != 60.0 {
		t.Errorf("expected 60.0ms tail penalty, got %f", diag.EstTailPenaltyMs)
	}
	if !strings.Contains(diag.PSIHeadroom, "artificial CFS period artifact") {
		t.Errorf("expected PSI headroom to indicate artificial CFS artifact, got %s", diag.PSIHeadroom)
	}
	if !strings.Contains(diag.Recommendation, "10ms") {
		t.Errorf("expected recommendation to suggest 10ms, got %s", diag.Recommendation)
	}
}

func TestParsePressure(t *testing.T) {
	raw := `some avg10=2.45 avg60=1.80 avg300=0.95 total=12345678
full avg10=0.00 avg60=0.00 avg300=0.00 total=0
`
	psi, err := ParsePressure(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("ParsePressure failed: %v", err)
	}
	if psi.Some.Avg10 != 2.45 {
		t.Errorf("expected Some.Avg10=2.45, got %f", psi.Some.Avg10)
	}
	if psi.Some.Avg60 != 1.80 {
		t.Errorf("expected Some.Avg60=1.80, got %f", psi.Some.Avg60)
	}
	if psi.Some.Total != 12345678 {
		t.Errorf("expected Some.Total=12345678, got %d", psi.Some.Total)
	}
	if psi.Full.Avg10 != 0.0 {
		t.Errorf("expected Full.Avg10=0.0, got %f", psi.Full.Avg10)
	}
}

func TestExtractIdentifier(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/sys/fs/cgroup/kubepods.slice/kubepods-burstable.slice/podabc-123/container1", "podabc-123"},
		{"/sys/fs/cgroup/system.slice/docker-xyz.scope", "docker-xyz.scope"},
		{"/sys/fs/cgroup/mygroup", "mygroup"},
	}

	for _, tt := range tests {
		got := ExtractIdentifier(tt.path)
		if got != tt.want {
			t.Errorf("ExtractIdentifier(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
