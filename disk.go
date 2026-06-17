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

type diskFillPredictor struct {
	previous map[string]diskUsage
}

type diskUsage struct {
	name      string
	total     float64
	used      float64
	timestamp time.Time
}

func newDiskFillPredictor() *diskFillPredictor {
	return &diskFillPredictor{previous: make(map[string]diskUsage)}
}

func (p *diskFillPredictor) collect(samples []MetricSample, within time.Duration) []MetricSample {
	if within <= 0 {
		return nil
	}

	current := diskUsages(samples)
	out := make([]MetricSample, 0, len(current))
	for name, usage := range current {
		value := float64(0)
		if previous, ok := p.previous[name]; ok {
			if fillsWithin(previous, usage, within) {
				value = 1
			}
		}
		out = append(out, MetricSample{
			Name:      "disk." + name + ".fills_within_window",
			Value:     value,
			Unit:      "state",
			Timestamp: usage.timestamp,
			Collector: "disk",
		})
		p.previous[name] = usage
	}
	return out
}

func diskUsages(samples []MetricSample) map[string]diskUsage {
	usages := make(map[string]diskUsage)
	for _, sample := range samples {
		if sample.Collector != "disk" || !strings.HasPrefix(sample.Name, "disk.") {
			continue
		}
		name, field, ok := splitDiskMetric(sample.Name)
		if !ok {
			continue
		}
		usage := usages[name]
		usage.name = name
		usage.timestamp = sample.Timestamp
		switch field {
		case "total":
			usage.total = sample.Value
		case "used":
			usage.used = sample.Value
		}
		usages[name] = usage
	}
	for name, usage := range usages {
		if usage.total <= 0 || usage.timestamp.IsZero() {
			delete(usages, name)
		}
	}
	return usages
}

func splitDiskMetric(metric string) (string, string, bool) {
	rest := strings.TrimPrefix(metric, "disk.")
	i := strings.LastIndexByte(rest, '.')
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

func fillsWithin(previous diskUsage, current diskUsage, within time.Duration) bool {
	elapsed := current.timestamp.Sub(previous.timestamp).Seconds()
	if elapsed <= 0 || current.total <= current.used {
		return false
	}
	usedDelta := current.used - previous.used
	if usedDelta <= 0 {
		return false
	}
	secondsUntilFull := (current.total - current.used) / (usedDelta / elapsed)
	return secondsUntilFull > 0 && secondsUntilFull <= within.Seconds()
}

func mountPointToName(mp string) string {
	if mp == "/" {
		return "root"
	}
	return safeMetricPart(strings.TrimPrefix(mp, "/"))
}
