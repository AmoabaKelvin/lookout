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

	collectedAt := time.Now()
	memUsed := memTotal - memAvailable
	swapTotal := memInfo["SwapTotal"]
	swapFree := memInfo["SwapFree"]
	if swapFree < 0 {
		swapFree = 0
	}
	if swapFree > swapTotal {
		swapFree = swapTotal
	}
	swapUsed := swapTotal - swapFree
	var swapUsedPercent float64
	if swapTotal > 0 {
		swapUsedPercent = (swapUsed / swapTotal) * 100
	}

	return []MetricSample{
		{
			Name:      "memory.total",
			Value:     memTotal,
			Unit:      "kB",
			Timestamp: collectedAt,
			Collector: "memory",
		},
		{
			Name:      "memory.used_percent",
			Value:     (memUsed / memTotal) * 100,
			Unit:      "percent",
			Timestamp: collectedAt,
			Collector: "memory",
		},
		{
			Name:      "memory.available",
			Value:     memAvailable,
			Unit:      "kB",
			Timestamp: collectedAt,
			Collector: "memory",
		},
		{
			Name:      "memory.used",
			Value:     memUsed,
			Unit:      "kB",
			Timestamp: collectedAt,
			Collector: "memory",
		},
		{
			Name:      "swap.total",
			Value:     swapTotal,
			Unit:      "kB",
			Timestamp: collectedAt,
			Collector: "swap",
		},
		{
			Name:      "swap.used_percent",
			Value:     swapUsedPercent,
			Unit:      "percent",
			Timestamp: collectedAt,
			Collector: "swap",
		},
		{
			Name:      "swap.free",
			Value:     swapFree,
			Unit:      "kB",
			Timestamp: collectedAt,
			Collector: "swap",
		},
		{
			Name:      "swap.used",
			Value:     swapUsed,
			Unit:      "kB",
			Timestamp: collectedAt,
			Collector: "swap",
		},
	}, nil
}

func parseMemInfo(data []byte, source string) (map[string]float64, error) {
	memInfo := make(map[string]float64)

	// /proc/meminfo lines look like "MemTotal:  8185712 kB"
	for i, part := range strings.Split(string(data), "\n") {
		lineNumber := i + 1
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

	fallback := func(key string) (float64, error) {
		value, err := requiredMemInfoValue(memInfo, key, source)
		if err != nil {
			return 0, fmt.Errorf("%s: MemAvailable missing and fallback field %s unavailable: %w", source, key, err)
		}
		return value, nil
	}

	memFree, err := fallback("MemFree")
	if err != nil {
		return 0, err
	}
	buffers, err := fallback("Buffers")
	if err != nil {
		return 0, err
	}
	cached, err := fallback("Cached")
	if err != nil {
		return 0, err
	}

	sReclaimable := memInfo["SReclaimable"]
	shmem := memInfo["Shmem"]
	return memFree + buffers + cached + sReclaimable - shmem, nil
}
