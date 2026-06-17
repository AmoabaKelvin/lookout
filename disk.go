package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func diskCollector(path string, targetMounts []string) ([]MetricSample, error) {
	collectedAt := time.Now()
	var samples []MetricSample

	mountsData, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}

	targetSet := make(map[string]bool, len(targetMounts))
	for _, m := range targetMounts {
		targetSet[m] = true
	}

	lines := strings.Split(strings.TrimSpace(string(mountsData)), "\n")
	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		mountPoint := fields[1]

		if len(targetMounts) > 0 && !targetSet[mountPoint] {
			continue
		}

		var stat unix.Statfs_t
		if err := unix.Statfs(mountPoint, &stat); err != nil {
			continue
		}

		if stat.Bsize <= 0 || stat.Blocks == 0 {
			continue
		}

		blockSize := uint64(stat.Bsize)
		total := stat.Blocks * blockSize
		free := stat.Bfree * blockSize
		used := total - free
		usedPercent := (float64(used) / float64(total)) * 100

		name := mountPointToName(mountPoint)
		samples = append(samples,
			MetricSample{Name: "disk." + name + ".total", Value: float64(total), Unit: "bytes", Timestamp: collectedAt, Collector: "disk"},
			MetricSample{Name: "disk." + name + ".used", Value: float64(used), Unit: "bytes", Timestamp: collectedAt, Collector: "disk"},
			MetricSample{Name: "disk." + name + ".free", Value: float64(free), Unit: "bytes", Timestamp: collectedAt, Collector: "disk"},
			MetricSample{Name: "disk." + name + ".used_percent", Value: usedPercent, Unit: "percent", Timestamp: collectedAt, Collector: "disk"},
		)
	}

	return samples, nil
}

func mountPointToName(mp string) string {
	if mp == "/" {
		return "root"
	}
	return safeMetricPart(strings.TrimPrefix(mp, "/"))
}
