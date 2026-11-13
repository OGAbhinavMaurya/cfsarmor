package sentry

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// DefaultPSIPath is the kernel sysfs location for CPU pressure stall information.
const DefaultPSIPath = "/proc/pressure/cpu"

// PSIMetric holds pressure stall percentages and total stalled time.
type PSIMetric struct {
	Avg10  float64 `json:"avg10"`  // 10-second moving average stall percentage
	Avg60  float64 `json:"avg60"`  // 60-second moving average stall percentage
	Avg300 float64 `json:"avg300"` // 300-second moving average stall percentage
	Total  uint64  `json:"total"`  // Total stall duration in microseconds
}

// PSIStatus captures CPU pressure metrics for 'some' and 'full' stalls.
type PSIStatus struct {
	Some PSIMetric `json:"some"`
	Full PSIMetric `json:"full"`
}

// ReadPressure opens and parses the specified PSI file (defaults to /proc/pressure/cpu).
func ReadPressure(path string) (*PSIStatus, error) {
	if path == "" {
		path = DefaultPSIPath
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening pressure file %s: %w", path, err)
	}
	defer f.Close()

	return ParsePressure(f)
}

// ParsePressure parses PSI text output from an io.Reader.
func ParsePressure(r io.Reader) (*PSIStatus, error) {
	status := &PSIStatus{}
	scanner := bufio.NewScanner(r)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		stallType := fields[0]
		var metric *PSIMetric
		switch stallType {
		case "some":
			metric = &status.Some
		case "full":
			metric = &status.Full
		default:
			continue
		}

		for _, kv := range fields[1:] {
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) != 2 {
				continue
			}
			k, v := parts[0], parts[1]
			switch k {
			case "avg10":
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					metric.Avg10 = f
				}
			case "avg60":
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					metric.Avg60 = f
				}
			case "avg300":
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					metric.Avg300 = f
				}
			case "total":
				if u, err := strconv.ParseUint(v, 10, 64); err == nil {
					metric.Total = u
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parsing pressure data: %w", err)
	}

	return status, nil
}
