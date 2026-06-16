package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func loadCollector(path string) ([]MetricSample, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}

	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return nil, fmt.Errorf("%s: expected at least 3 load average fields, got %d", path, len(fields))
	}

	loads := make([]float64, 3)
	for i := range loads {
		load, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return nil, fmt.Errorf("%s: parsing load average field %d value %q: %w", path, i+1, fields[i], err)
		}
		loads[i] = load
	}

	collectedAt := time.Now()
	return []MetricSample{
		{Name: "load.1", Value: loads[0], Unit: "load", Timestamp: collectedAt, Collector: "load"},
		{Name: "load.5", Value: loads[1], Unit: "load", Timestamp: collectedAt, Collector: "load"},
		{Name: "load.15", Value: loads[2], Unit: "load", Timestamp: collectedAt, Collector: "load"},
	}, nil
}
