package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type cpuTimes struct {
	idle  uint64
	total uint64
}

func cpuCollector(path string, previous *cpuTimes) ([]MetricSample, *cpuTimes, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, previous, fmt.Errorf("failed to read %s: %w", path, err)
	}

	current, err := parseCPUTimes(data, path)
	if err != nil {
		return nil, previous, err
	}
	if previous == nil {
		return nil, &current, nil
	}
	if current.total <= previous.total || current.idle < previous.idle {
		return nil, &current, nil
	}

	totalDelta := current.total - previous.total
	idleDelta := current.idle - previous.idle
	usedPercent := (float64(totalDelta-idleDelta) / float64(totalDelta)) * 100

	return []MetricSample{{
		Name:      "cpu.used_percent",
		Value:     usedPercent,
		Unit:      "percent",
		Timestamp: time.Now(),
		Collector: "cpu",
	}}, &current, nil
}

func parseCPUTimes(data []byte, source string) (cpuTimes, error) {
	for lineNumber, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "cpu" {
			continue
		}
		if len(fields) < 5 {
			return cpuTimes{}, fmt.Errorf("%s:%d: expected at least 4 cpu fields, got %d", source, lineNumber+1, len(fields)-1)
		}

		values := make([]uint64, len(fields)-1)
		for i := range values {
			value, err := strconv.ParseUint(fields[i+1], 10, 64)
			if err != nil {
				return cpuTimes{}, fmt.Errorf("%s:%d: parsing cpu field %d value %q: %w", source, lineNumber+1, i+1, fields[i+1], err)
			}
			values[i] = value
		}

		var total uint64
		for _, value := range values {
			total += value
		}
		idle := values[3]
		if len(values) > 4 {
			idle += values[4] // iowait is idle time from the CPU utilization perspective.
		}
		return cpuTimes{idle: idle, total: total}, nil
	}
	return cpuTimes{}, fmt.Errorf("%s: missing aggregate cpu line", source)
}
