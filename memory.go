package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func memoryCollector(path string) ([]MetricSample, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}

	memInfo, err := parseMemInfo(data, path)
	if err != nil {
		return nil, err
	}

	memTotal, err := requiredMemInfoValue(memInfo, "MemTotal", path)
	if err != nil {
		return nil, err
	}
	if memTotal <= 0 {
		return nil, fmt.Errorf("%s: MemTotal must be positive, got %.0f kB", path, memTotal)
	}

	memAvailable, err := memAvailableValue(memInfo, path)
	if err != nil {
		return nil, err
	}
	if memAvailable < 0 {
		return nil, fmt.Errorf("%s: available memory cannot be negative, got %.0f kB", path, memAvailable)
	}
	if memAvailable > memTotal {
		memAvailable = memTotal
	}

	timeCollected := time.Now()
	memUsed := memTotal - memAvailable

	return []MetricSample{
		{
			Name:      "memory.total",
			Value:     memTotal,
			Unit:      "kB",
			Timestamp: timeCollected,
			Collector: "memory",
		},
		{
			Name:      "memory.used_percent",
			Value:     (memUsed / memTotal) * 100,
			Unit:      "percent",
			Timestamp: timeCollected,
			Collector: "memory",
		},
		{
			Name:      "memory.available",
			Value:     memAvailable,
			Unit:      "kB",
			Timestamp: timeCollected,
			Collector: "memory",
		},
		{
			Name:      "memory.used",
			Value:     memUsed,
			Unit:      "kB",
			Timestamp: timeCollected,
			Collector: "memory",
		},
	}, nil
}

func parseMemInfo(data []byte, source string) (map[string]float64, error) {
	memInfo := make(map[string]float64)

	// /proc/meminfo lines look like "MemTotal:  8185712 kB"
	parts := strings.SplitSeq(string(data), "\n")
	lineNumber := 0
	for part := range parts {
		lineNumber++
		if part == "" {
			continue
		}
		keyValueSplit := strings.SplitN(part, ":", 2)

		if len(keyValueSplit) != 2 {
			return nil, fmt.Errorf("%s:%d: expected key/value pair, got %q", source, lineNumber, part)
		}

		key := strings.TrimSpace(keyValueSplit[0])
		fields := strings.Fields(keyValueSplit[1])
		if key == "" || len(fields) == 0 {
			return nil, fmt.Errorf("%s:%d: missing value for %q", source, lineNumber, key)
		}

		value, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: parsing %s value %q: %w", source, lineNumber, key, fields[0], err)
		}
		memInfo[key] = value
	}

	return memInfo, nil
}

func requiredMemInfoValue(memInfo map[string]float64, key string, source string) (float64, error) {
	value, ok := memInfo[key]
	if !ok {
		return 0, fmt.Errorf("%s: missing required memory field %s", source, key)
	}
	return value, nil
}

func memAvailableValue(memInfo map[string]float64, source string) (float64, error) {
	if value, ok := memInfo["MemAvailable"]; ok {
		return value, nil
	}

	memFree, err := requiredMemInfoValue(memInfo, "MemFree", source)
	if err != nil {
		return 0, fmt.Errorf("%s: missing MemAvailable and fallback field MemFree", source)
	}
	buffers, err := requiredMemInfoValue(memInfo, "Buffers", source)
	if err != nil {
		return 0, fmt.Errorf("%s: missing MemAvailable and fallback field Buffers", source)
	}
	cached, err := requiredMemInfoValue(memInfo, "Cached", source)
	if err != nil {
		return 0, fmt.Errorf("%s: missing MemAvailable and fallback field Cached", source)
	}

	sReclaimable := memInfo["SReclaimable"]
	shmem := memInfo["Shmem"]
	return memFree + buffers + cached + sReclaimable - shmem, nil
}
