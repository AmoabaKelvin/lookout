package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// pressureLine is one "some" or "full" line from a PSI resource file.
// Total is cumulative stall time in microseconds since boot.
type pressureLine struct {
	Avg10  float64
	Avg60  float64
	Avg300 float64
	Total  uint64
}

type pressureResource struct {
	Some *pressureLine
	Full *pressureLine
}

type pressureStats struct {
	CPU    pressureResource
	Memory pressureResource
	IO     pressureResource
}

type pressureSignal struct {
	resource string
	scope    string
	line     pressureLine
}

// pressureCollector keeps the previous cumulative totals so it can report the
// exact percentage of wall time lost between Lookout collection passes. The
// kernel-provided averages are reported alongside that interval measurement.
type pressureCollector struct {
	dir        string
	previousAt time.Time
	previous   map[string]uint64
	now        func() time.Time
}

func newPressureCollector(dir string) (*pressureCollector, error) {
	// Probe and parse all required files once so an explicitly enabled alert
	// cannot silently run without its signal.
	if _, err := readPressureStats(dir); err != nil {
		return nil, err
	}
	return &pressureCollector{
		dir:      dir,
		previous: make(map[string]uint64),
		now:      time.Now,
	}, nil
}

func (c *pressureCollector) Collect() ([]MetricSample, error) {
	stats, err := readPressureStats(c.dir)
	if err != nil {
		return nil, err
	}

	collectedAt := c.now()
	signals := pressureSignals(stats)
	samples := make([]MetricSample, 0, len(signals)*4)

	for _, signal := range signals {
		prefix := "pressure." + signal.resource + "." + signal.scope
		for _, avg := range []struct {
			name  string
			value float64
		}{
			{name: "avg10_percent", value: signal.line.Avg10},
			{name: "avg60_percent", value: signal.line.Avg60},
			{name: "avg300_percent", value: signal.line.Avg300},
		} {
			samples = append(samples, MetricSample{
				Name:      prefix + "." + avg.name,
				Value:     avg.value,
				Unit:      "percent",
				Timestamp: collectedAt,
				Collector: "pressure",
			})
		}

		key := signal.resource + "." + signal.scope
		previousTotal, hasPrevious := c.previous[key]
		if hasPrevious && !c.previousAt.IsZero() && collectedAt.After(c.previousAt) && signal.line.Total >= previousTotal {
			elapsedMicros := float64(collectedAt.Sub(c.previousAt).Microseconds())
			if elapsedMicros > 0 {
				stallPercent := (float64(signal.line.Total-previousTotal) / elapsedMicros) * 100
				// PSI is a wall-time proportion. Protect alerts from tiny timing or
				// rounding discrepancies around the upper bound.
				if stallPercent > 100 {
					stallPercent = 100
				}
				samples = append(samples, MetricSample{
					Name:      prefix + ".stall_percent",
					Value:     stallPercent,
					Unit:      "percent",
					Timestamp: collectedAt,
					Collector: "pressure",
				})
			}
		}
	}

	for _, signal := range signals {
		c.previous[signal.resource+"."+signal.scope] = signal.line.Total
	}
	c.previousAt = collectedAt
	return samples, nil
}

func pressureSignals(stats pressureStats) []pressureSignal {
	signals := []pressureSignal{
		{resource: "cpu", scope: "some", line: *stats.CPU.Some},
		{resource: "memory", scope: "some", line: *stats.Memory.Some},
		{resource: "memory", scope: "full", line: *stats.Memory.Full},
		{resource: "io", scope: "some", line: *stats.IO.Some},
		{resource: "io", scope: "full", line: *stats.IO.Full},
	}
	return signals
}

func readPressureStats(dir string) (pressureStats, error) {
	read := func(resource string, requireFull bool) (pressureResource, error) {
		path := filepath.Join(dir, resource)
		data, err := os.ReadFile(path)
		if err != nil {
			return pressureResource{}, fmt.Errorf("failed to read %s: %w", path, err)
		}
		return parsePressureResource(data, path, requireFull)
	}

	cpu, err := read("cpu", false)
	if err != nil {
		return pressureStats{}, err
	}
	memory, err := read("memory", true)
	if err != nil {
		return pressureStats{}, err
	}
	ioStats, err := read("io", true)
	if err != nil {
		return pressureStats{}, err
	}

	return pressureStats{CPU: cpu, Memory: memory, IO: ioStats}, nil
}

func parsePressureResource(data []byte, source string, requireFull bool) (pressureResource, error) {
	var resource pressureResource
	for i, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			return pressureResource{}, fmt.Errorf("%s:%d: malformed pressure line %q", source, i+1, rawLine)
		}

		scope := fields[0]
		if scope != "some" && scope != "full" {
			return pressureResource{}, fmt.Errorf("%s:%d: unknown pressure scope %q", source, i+1, scope)
		}

		parsed, err := parsePressureLine(fields[1:], source, i+1)
		if err != nil {
			return pressureResource{}, err
		}
		switch scope {
		case "some":
			if resource.Some != nil {
				return pressureResource{}, fmt.Errorf("%s:%d: duplicate some pressure line", source, i+1)
			}
			resource.Some = &parsed
		case "full":
			if resource.Full != nil {
				return pressureResource{}, fmt.Errorf("%s:%d: duplicate full pressure line", source, i+1)
			}
			resource.Full = &parsed
		}
	}

	if resource.Some == nil {
		return pressureResource{}, fmt.Errorf("%s: missing some pressure line", source)
	}
	if requireFull && resource.Full == nil {
		return pressureResource{}, fmt.Errorf("%s: missing full pressure line", source)
	}
	return resource, nil
}

func parsePressureLine(fields []string, source string, lineNumber int) (pressureLine, error) {
	values := make(map[string]string, len(fields))
	for _, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok || key == "" || value == "" {
			return pressureLine{}, fmt.Errorf("%s:%d: malformed pressure field %q", source, lineNumber, field)
		}
		if _, exists := values[key]; exists {
			return pressureLine{}, fmt.Errorf("%s:%d: duplicate pressure field %q", source, lineNumber, key)
		}
		values[key] = value
	}

	parseAverage := func(key string) (float64, error) {
		raw, ok := values[key]
		if !ok {
			return 0, fmt.Errorf("%s:%d: missing pressure field %s", source, lineNumber, key)
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0, fmt.Errorf("%s:%d: parsing %s value %q: %w", source, lineNumber, key, raw, err)
		}
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
			return 0, fmt.Errorf("%s:%d: %s must be between 0 and 100, got %v", source, lineNumber, key, value)
		}
		return value, nil
	}

	avg10, err := parseAverage("avg10")
	if err != nil {
		return pressureLine{}, err
	}
	avg60, err := parseAverage("avg60")
	if err != nil {
		return pressureLine{}, err
	}
	avg300, err := parseAverage("avg300")
	if err != nil {
		return pressureLine{}, err
	}

	rawTotal, ok := values["total"]
	if !ok {
		return pressureLine{}, fmt.Errorf("%s:%d: missing pressure field total", source, lineNumber)
	}
	total, err := strconv.ParseUint(rawTotal, 10, 64)
	if err != nil {
		return pressureLine{}, fmt.Errorf("%s:%d: parsing total value %q: %w", source, lineNumber, rawTotal, err)
	}

	return pressureLine{Avg10: avg10, Avg60: avg60, Avg300: avg300, Total: total}, nil
}
