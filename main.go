package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type MetricSample struct {
	Name      string
	Value     float64
	Unit      string
	Timestamp time.Time
	Collector string
}

// type Collector interface {
// 	Collect(ctx context.Context) ([]MetricSample, error)
// }

func memoryCollector(path string) ([]MetricSample, error) {
	memInfo := make(map[string]float64)
	data, err := os.ReadFile(path)
	if err != nil {
		return []MetricSample{}, errors.New("There was an issue reading the /proc/memory file")
	}

	// proc/memory has key value pairs, separated by : and the values have KB
	parts := strings.SplitSeq(string(data), "\n")
	// convert the data into a map of key value pairs and then we read each of those key value stuff
	for part := range parts {
		if part == "" {
			continue
		}
		keyValueSplit := strings.SplitN(part, ":", 2)

		if len(keyValueSplit) != 2 {
			continue
		}

		key := strings.TrimSpace(keyValueSplit[0])
		value, err := strconv.Atoi(
			strings.TrimSpace(strings.Split(strings.TrimSpace(keyValueSplit[1]), " ")[0]),
		)
		if err != nil {
			fmt.Println("Something went wrong")
		}

		memInfo[key] = float64(value)

	}

	timeCollected := time.Now()
	memUsed := memInfo["MemTotal"] - memInfo["MemAvailable"]

	var metricsCollected []MetricSample

	metricsCollected = append(metricsCollected, MetricSample{
		Name:      "memory.total",
		Value:     memInfo["MemTotal"],
		Unit:      "kB",
		Timestamp: timeCollected,
		Collector: "memory",
	})

	metricsCollected = append(metricsCollected, MetricSample{
		Name:      "memory.used_percent",
		Value:     (memUsed / memInfo["MemTotal"]) * 100,
		Unit:      "percent",
		Timestamp: timeCollected,
		Collector: "memory",
	})

	metricsCollected = append(metricsCollected, MetricSample{
		Name:      "memory.available",
		Value:     memInfo["MemAvailable"],
		Unit:      "kB",
		Timestamp: timeCollected,
		Collector: "memory",
	})

	metricsCollected = append(metricsCollected, MetricSample{
		Name:      "memory.used",
		Value:     memUsed,
		Unit:      "kB",
		Timestamp: timeCollected,
		Collector: "memory",
	})

	return metricsCollected, nil
}

// i think we are going to have to discuss how we will deal with the different
// types we will have, so keeping it this nested map
func diskCollector(path string) ([]MetricSample, error) {
	timeCollected := time.Now()
	var metricsCollected []MetricSample

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("There was an issue reading the file")
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")

	for _, line := range lines {
		fields := strings.Fields(line)

		if len(fields) < 6 {
			continue
		}

		if fields[0] == "Filesystem" {
			continue
		}

		device := fields[0]

		stats := []struct {
			name   string
			value  string
			unit   string
			parser func(string) float64
		}{
			{"used_percent", strings.TrimSuffix(fields[4], "%"), "", parseFloat},
			{"used", fields[2], "bytes", parseSize},
			{"available", fields[3], "bytes", parseSize},
		}

		for _, s := range stats {
			metricsCollected = append(metricsCollected, MetricSample{
				Name:      "disk." + device + "." + s.name,
				Value:     s.parser(s.value),
				Unit:      s.unit,
				Timestamp: timeCollected,
				Collector: "disk",
			})
		}
	}

	return metricsCollected, nil
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// different units returned from df so I had to parse them
func parseSize(s string) float64 {
	if s == "0" {
		return 0
	}
	units := map[byte]float64{
		'K': 1024,
		'M': 1024 * 1024,
		'G': 1024 * 1024 * 1024,
		'T': 1024 * 1024 * 1024 * 1024,
	}
	last := s[len(s)-1]
	if mult, ok := units[last]; ok {
		v, _ := strconv.ParseFloat(s[:len(s)-1], 64)
		return v * mult
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func main() {
	meminfoPath := os.Getenv("MEMINFO_PATH")
	if meminfoPath == "" {
		meminfoPath = "meminfo.txt"
	}

	diskInfoPath := os.Getenv("DISKINFO_PATH")
	if diskInfoPath == "" {
		diskInfoPath = "df.txt"
	}

	// we should have a way to gather all collectors and then run a common method,
	// eg, collect on them all (common Collector interface). and they will run
	// independently to collect their data

	ticker := time.NewTicker(time.Duration(5) * time.Second)
	defer ticker.Stop()

	// this channel will be the pipe the evaluator reads from.
	// the evaluator reads the things coming
	// in and then decides based on teh things we wanted to track whether or not
	// to go ahead to read certain values. then assuming something crosses a certain
	// configured threshold, it goes ahead to create an alert
	incomingSamplesChannel := make(chan MetricSample, 100)

	go func() {
		for metric := range incomingSamplesChannel {
			fmt.Printf("  metric: %s = %.2f %s\n", metric.Name, metric.Value, metric.Unit)
			if metric.Name == "memory.used_percent" && metric.Value > 5 {
				fmt.Println("Memory usage is high")
			}
			if strings.HasPrefix(metric.Name, "disk.") &&
				strings.HasSuffix(metric.Name, ".used_percent") &&
				metric.Value > 50 {
				device := strings.TrimSuffix(strings.TrimPrefix(metric.Name, "disk."), ".used_percent")
				fmt.Printf("Disk %s usage is too high: %.0f%%\n", device, metric.Value)
			}
		}
	}()

	for range ticker.C {
		fmt.Println("Running memory collection")
		data, err := memoryCollector(meminfoPath)
		if err != nil {
			fmt.Printf("Error collecting memory info: %v\n", err)
			continue
		}
		for _, d := range data {
			incomingSamplesChannel <- d
		}

		fmt.Println("Running disk collection")
		diskData, err := diskCollector(diskInfoPath)
		if err != nil {
			fmt.Printf("Error collecting disk info: %v\n", err)
			continue
		}
		for _, d := range diskData {
			incomingSamplesChannel <- d
		}
	}
}
