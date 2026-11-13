package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/OGAbhinavMaurya/cfsarmor/pkg/cgroup"
	"github.com/OGAbhinavMaurya/cfsarmor/pkg/sentry"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "doctor":
		if err := runDoctor(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "tune":
		if err := runTune(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "version", "--version", "-v":
		fmt.Printf("cfsarmor %s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`cfsarmor: Linux CFS bandwidth throttling sentry and quota tuner

Usage:
  cfsarmor doctor [flags]   Inspect cgroups for scheduler throttling and latency impact
  cfsarmor tune [flags]     Adjust CFS period and burst parameters with proportional quota scaling
  cfsarmor version          Display version information

Flags for 'doctor':
  --path string        Root cgroup directory to scan (default "/sys/fs/cgroup")
  --threshold float    Throttling percentage threshold to flag (default 5.0)
  --sample duration    Sampling duration for delta inspection (e.g. 1s, 2s; default 0s)
  --all                Display all scanned cgroups including unthrottled groups
  --json               Output assessment in JSON format

Flags for 'tune':
  --cgroup string      Target cgroup directory path
  --pod string         Target pod or container name substring
  --path string        Root directory to search when using --pod (default "/sys/fs/cgroup")
  --period string      Target CFS period duration (e.g. 10ms, 20ms; default "10ms")
  --burst string       Target burst allowance as percentage of quota (e.g. 50%, 100%)
  --dry-run            Calculate and display changes without applying them
`)
}

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	cgroupPath := fs.String("path", "/sys/fs/cgroup", "Root cgroup directory")
	threshold := fs.Float64("threshold", 5.0, "Throttling threshold percentage")
	sample := fs.Duration("sample", 0, "Sampling interval for delta analysis")
	showAll := fs.Bool("all", false, "Include healthy cgroups in output")
	jsonOut := fs.Bool("json", false, "Output results in JSON")

	if err := fs.Parse(args); err != nil {
		return err
	}

	cgroups, err := cgroup.FindCgroups(*cgroupPath)
	if err != nil {
		return fmt.Errorf("scanning cgroups in %s: %w", *cgroupPath, err)
	}

	psi, _ := sentry.ReadPressure("")

	var diags []*sentry.Diagnosis
	if *sample > 0 {
		diags, err = sentry.DiagnoseDelta(cgroups, *sample, *threshold, psi)
		if err != nil {
			return fmt.Errorf("delta sampling failed: %w", err)
		}
	} else {
		for _, cg := range cgroups {
			diags = append(diags, sentry.Diagnose(cg, *threshold, psi))
		}
	}

	var filtered []*sentry.Diagnosis
	for _, d := range diags {
		if *showAll || d.Level == sentry.LevelThrottled || d.Level == sentry.LevelWarning {
			filtered = append(filtered, d)
		}
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(filtered)
	}

	if len(filtered) == 0 {
		fmt.Printf("Scanned %d cgroups. No throttling detected above %.1f%% threshold.\n", len(cgroups), *threshold)
		if psi != nil {
			fmt.Printf("Node PSI: some avg10=%.2f%%, full avg10=%.2f%%\n", psi.Some.Avg10, psi.Full.Avg10)
		}
		return nil
	}

	fmt.Printf("CFS Throttling Report (scanned %d cgroups):\n", len(cgroups))
	if psi != nil {
		fmt.Printf("Host CPU Pressure: some avg10=%.2f%%, avg60=%.2f%%\n\n", psi.Some.Avg10, psi.Some.Avg60)
	}

	for _, d := range filtered {
		fmt.Printf("[%s] %s\n", d.Level, d.ContainerName)
		fmt.Printf("  Path:            %s\n", d.CgroupPath)
		fmt.Printf("  Throttled:       %.1f%% of periods (%d / %d periods)\n",
			d.ThrottleRatio, d.ThrottledCount, d.TotalPeriods)
		fmt.Printf("  Average Freeze:  %.1f ms per throttled period\n", d.AvgFreezeMs)
		fmt.Printf("  P99 Penalty:     +%.1f ms estimated tail latency impact\n", d.EstTailPenaltyMs)
		fmt.Printf("  Period / Quota:  %s / %s\n",
			cgroup.FormatDurationUsec(d.PeriodUsec), cgroup.FormatDurationUsec(d.QuotaUsec))
		if d.PSIHeadroom != "" {
			fmt.Printf("  Context:         %s\n", d.PSIHeadroom)
		}
		if d.Recommendation != "" {
			fmt.Printf("  Remediation:     %s\n", d.Recommendation)
		}
		fmt.Println()
	}

	return nil
}

func runTune(args []string) error {
	fs := flag.NewFlagSet("tune", flag.ContinueOnError)
	cgroupPath := fs.String("cgroup", "", "Target cgroup directory")
	podName := fs.String("pod", "", "Target pod name substring")
	rootPath := fs.String("path", "/sys/fs/cgroup", "Root directory for pod lookup")
	periodStr := fs.String("period", "10ms", "Target CFS period")
	burstStr := fs.String("burst", "", "Target burst percentage of quota (e.g. 50%)")
	dryRun := fs.Bool("dry-run", false, "Preview changes without applying")

	if err := fs.Parse(args); err != nil {
		return err
	}

	targetDir := *cgroupPath
	if targetDir == "" && *podName != "" {
		found, err := findCgroupByPod(*rootPath, *podName)
		if err != nil {
			return err
		}
		targetDir = found
	}

	if targetDir == "" {
		return errors.New("must specify --cgroup <path> or --pod <name>")
	}

	targetPeriodUsec, err := cgroup.ParseDurationUsec(*periodStr)
	if err != nil {
		return fmt.Errorf("parsing target period %q: %w", *periodStr, err)
	}

	// Detect version and read current state
	version := cgroup.DetectVersion(targetDir)
	if version == cgroup.Unknown {
		return fmt.Errorf("directory %s does not contain recognized cgroup files", targetDir)
	}

	var current *cgroup.Cgroup
	if version == cgroup.V2 {
		current, err = cgroup.ReadV2(targetDir)
	} else {
		current, err = cgroup.ReadV1(targetDir)
	}
	if err != nil {
		return fmt.Errorf("reading cgroup state: %w", err)
	}

	if current.QuotaUsec == -1 {
		return fmt.Errorf("cgroup %s has unlimited quota; period tuning requires an active quota limit", targetDir)
	}

	newQuotaUsec, err := cgroup.ScaleQuota(current.QuotaUsec, current.PeriodUsec, targetPeriodUsec)
	if err != nil {
		return fmt.Errorf("scaling quota: %w", err)
	}

	var newBurstUsec int64 = -1
	if *burstStr != "" {
		bPct, err := parseBurstPercentage(*burstStr)
		if err != nil {
			return err
		}
		newBurstUsec = int64(float64(newQuotaUsec) * bPct / 100.0)
	}

	fmt.Printf("[TUNE] %s (%s)\n", targetDir, version)
	fmt.Printf("  Period: %s -> %s\n",
		cgroup.FormatDurationUsec(current.PeriodUsec),
		cgroup.FormatDurationUsec(targetPeriodUsec))
	fmt.Printf("  Quota:  %s -> %s (ratio %.2fx preserved)\n",
		cgroup.FormatDurationUsec(current.QuotaUsec),
		cgroup.FormatDurationUsec(newQuotaUsec),
		float64(current.QuotaUsec)/float64(current.PeriodUsec))
	if newBurstUsec >= 0 {
		fmt.Printf("  Burst:  %s -> %s\n",
			cgroup.FormatDurationUsec(current.BurstUsec),
			cgroup.FormatDurationUsec(newBurstUsec))
	}

	if *dryRun {
		fmt.Println("Dry run enabled. No changes applied.")
		return nil
	}

	if version == cgroup.V2 {
		err = cgroup.WriteV2(targetDir, newQuotaUsec, targetPeriodUsec, newBurstUsec)
	} else {
		err = cgroup.WriteV1(targetDir, newQuotaUsec, targetPeriodUsec, newBurstUsec)
	}
	if err != nil {
		return fmt.Errorf("applying cgroup configuration: %w", err)
	}

	fmt.Println("Configuration successfully applied.")
	return nil
}

func findCgroupByPod(root, pod string) (string, error) {
	var match string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		if strings.Contains(path, pod) {
			// Check if this directory has cpu controls
			if _, e := os.Stat(filepath.Join(path, "cpu.max")); e == nil {
				match = path
				return filepath.SkipDir
			}
			if _, e := os.Stat(filepath.Join(path, "cpu.cfs_period_us")); e == nil {
				match = path
				return filepath.SkipDir
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if match == "" {
		return "", fmt.Errorf("no cgroup found matching pod name %q under %s", pod, root)
	}
	return match, nil
}

func parseBurstPercentage(s string) (float64, error) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(s, "%"))
	val, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid burst percentage %q: %w", s, err)
	}
	if val < 0 || val > 500 {
		return 0, fmt.Errorf("burst percentage must be between 0%% and 500%%, got %.1f%%", val)
	}
	return val, nil
}
