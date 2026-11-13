package cgroup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScaleQuota(t *testing.T) {
	tests := []struct {
		name       string
		oldQuota   int64
		oldPeriod  int64
		newPeriod  int64
		wantQuota  int64
		wantErr    bool
	}{
		{
			name:      "scale 2 cores from 100ms to 10ms",
			oldQuota:  200000,
			oldPeriod: 100000,
			newPeriod: 10000,
			wantQuota: 20000,
			wantErr:   false,
		},
		{
			name:      "scale 0.5 core from 100ms to 20ms",
			oldQuota:  50000,
			oldPeriod: 100000,
			newPeriod: 20000,
			wantQuota: 10000,
			wantErr:   false,
		},
		{
			name:      "unlimited quota stays unlimited",
			oldQuota:  -1,
			oldPeriod: 100000,
			newPeriod: 10000,
			wantQuota: -1,
			wantErr:   false,
		},
		{
			name:      "period below kernel minimum 1000us fails",
			oldQuota:  100000,
			oldPeriod: 100000,
			newPeriod: 500,
			wantQuota: 0,
			wantErr:   true,
		},
		{
			name:      "scaled quota below 1000us fails",
			oldQuota:  2000,
			oldPeriod: 100000,
			newPeriod: 10000,
			wantQuota: 0,
			wantErr:   true,
		},
		{
			name:      "invalid old period",
			oldQuota:  100000,
			oldPeriod: 0,
			newPeriod: 10000,
			wantQuota: 0,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ScaleQuota(tt.oldQuota, tt.oldPeriod, tt.newPeriod)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ScaleQuota() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.wantQuota {
				t.Errorf("ScaleQuota() = %d, want %d", got, tt.wantQuota)
			}
		})
	}
}

func TestParseDurationUsec(t *testing.T) {
	tests := []struct {
		input   string
		want    int64
		wantErr bool
	}{
		{"10ms", 10000, false},
		{"100ms", 100000, false},
		{"1s", 1000000, false},
		{"50000us", 50000, false},
		{"50000µs", 50000, false},
		{"25000", 25000, false},
		{"invalid", 0, true},
		{"", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseDurationUsec(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseDurationUsec(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseDurationUsec(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatDurationUsec(t *testing.T) {
	if got := FormatDurationUsec(-1); got != "unlimited" {
		t.Errorf("FormatDurationUsec(-1) = %q, want unlimited", got)
	}
	if got := FormatDurationUsec(10000); got != "10ms" {
		t.Errorf("FormatDurationUsec(10000) = %q, want 10ms", got)
	}
	if got := FormatDurationUsec(1000000); got != "1s" {
		t.Errorf("FormatDurationUsec(1000000) = %q, want 1s", got)
	}
	if got := FormatDurationUsec(1234); got != "1234µs" {
		t.Errorf("FormatDurationUsec(1234) = %q, want 1234µs", got)
	}
}

func TestParseV2CPUMax(t *testing.T) {
	tests := []struct {
		input      string
		wantQuota  int64
		wantPeriod int64
		wantErr    bool
	}{
		{"max 100000\n", -1, 100000, false},
		{"200000 100000", 200000, 100000, false},
		{"max", -1, 100000, false},
		{"", 0, 0, true},
		{"bad 100000", 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			q, p, err := ParseV2CPUMax(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseV2CPUMax(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if q != tt.wantQuota || p != tt.wantPeriod {
				t.Errorf("ParseV2CPUMax(%q) = (%d, %d), want (%d, %d)", tt.input, q, p, tt.wantQuota, tt.wantPeriod)
			}
		})
	}
}

func TestV1ReadWrite(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cgroup_v1_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	if err := os.WriteFile(filepath.Join(tmpDir, "cpu.cfs_period_us"), []byte("100000\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "cpu.cfs_quota_us"), []byte("200000\n"), 0644); err != nil {
		t.Fatal(err)
	}
	statContent := "nr_periods 100\nnr_throttled 25\nthrottled_time 500000000\n" // 500ms = 500,000 us
	if err := os.WriteFile(filepath.Join(tmpDir, "cpu.stat"), []byte(statContent), 0644); err != nil {
		t.Fatal(err)
	}

	cg, err := ReadV1(tmpDir)
	if err != nil {
		t.Fatalf("ReadV1 failed: %v", err)
	}
	if cg.QuotaUsec != 200000 || cg.PeriodUsec != 100000 {
		t.Errorf("ReadV1 quota/period mismatch: got %d/%d", cg.QuotaUsec, cg.PeriodUsec)
	}
	// Verify nanosecond to microsecond conversion
	if cg.Stat.ThrottledUsec != 500000 {
		t.Errorf("ReadV1 throttled_usec = %d, want 500000", cg.Stat.ThrottledUsec)
	}

	// Test WriteV1 shrinking
	if err := WriteV1(tmpDir, 20000, 10000, 0); err != nil {
		t.Fatalf("WriteV1 failed: %v", err)
	}
	cg2, err := ReadV1(tmpDir)
	if err != nil {
		t.Fatalf("ReadV1 after write failed: %v", err)
	}
	if cg2.QuotaUsec != 20000 || cg2.PeriodUsec != 10000 {
		t.Errorf("ReadV1 after write got %d/%d, want 20000/10000", cg2.QuotaUsec, cg2.PeriodUsec)
	}
}

func TestV2ReadWrite(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cgroup_v2_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	if err := os.WriteFile(filepath.Join(tmpDir, "cpu.max"), []byte("150000 100000\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "cpu.max.burst"), []byte("25000\n"), 0644); err != nil {
		t.Fatal(err)
	}
	statContent := "nr_periods 500\nnr_throttled 50\nthrottled_usec 125000\nnr_bursts 10\nburst_usec 5000\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "cpu.stat"), []byte(statContent), 0644); err != nil {
		t.Fatal(err)
	}

	cg, err := ReadV2(tmpDir)
	if err != nil {
		t.Fatalf("ReadV2 failed: %v", err)
	}
	if cg.QuotaUsec != 150000 || cg.PeriodUsec != 100000 || cg.BurstUsec != 25000 {
		t.Errorf("ReadV2 values mismatch: quota=%d period=%d burst=%d", cg.QuotaUsec, cg.PeriodUsec, cg.BurstUsec)
	}
	if cg.Stat.ThrottledUsec != 125000 || cg.Stat.NrBursts != 10 {
		t.Errorf("ReadV2 stat mismatch: throttled=%d bursts=%d", cg.Stat.ThrottledUsec, cg.Stat.NrBursts)
	}

	// Test WriteV2
	if err := WriteV2(tmpDir, 15000, 10000, 5000); err != nil {
		t.Fatalf("WriteV2 failed: %v", err)
	}
	cg2, err := ReadV2(tmpDir)
	if err != nil {
		t.Fatalf("ReadV2 after write failed: %v", err)
	}
	if cg2.QuotaUsec != 15000 || cg2.PeriodUsec != 10000 || cg2.BurstUsec != 5000 {
		t.Errorf("ReadV2 after write mismatch: quota=%d period=%d burst=%d", cg2.QuotaUsec, cg2.PeriodUsec, cg2.BurstUsec)
	}
}
