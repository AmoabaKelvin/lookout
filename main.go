package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
	"golang.org/x/sys/unix"
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

func dockerCollector(cli *client.Client, ctx context.Context) {
	f := make(client.Filters).Add("type", "container")
	fmt.Println("Listening to Docker container events...")

	for {
		result := cli.Events(ctx, client.EventsListOptions{Filters: f})
		done := false
		for !done {
			select {
			case <-ctx.Done():
				return
			// where messages are handled
			case event, ok := <-result.Messages:
				if !ok {
					done = true
				} else {
					name := event.Actor.Attributes["name"]
					timestamp := time.Unix(event.Time, 0).Format("15:04:05")
					fmt.Printf("[docker] [%s] %s → %s (ID: %.12s)\n",
						timestamp, event.Action, name, event.Actor.ID,
					)
					switch event.Action {
					case events.ActionDie:
						if exitCode := event.Actor.Attributes["exitCode"]; exitCode != "" {
							fmt.Printf("  └─ Exit Code: %s\n", exitCode)
						}
					case events.ActionOOM:
						fmt.Println("  ALERT: Container was OOM killed!")
					case events.ActionRestart:
						fmt.Println("  Container restarting (per restart policy)")
					}
				}
			case err, ok := <-result.Err:
				if !ok || err == nil {
					done = true
				} else {
					fmt.Printf("[docker] Event stream error: %v\n", err)
					done = true
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

func diskCollector(path string, targetMounts []string) ([]MetricSample, error) {
	timeCollected := time.Now()
	var metricsCollected []MetricSample

	mountsData, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}

	//bool is bigger than empty struct
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
		metricsCollected = append(metricsCollected,
			MetricSample{Name: "disk." + name + ".total", Value: float64(total), Unit: "bytes", Timestamp: timeCollected, Collector: "disk"},
			MetricSample{Name: "disk." + name + ".used", Value: float64(used), Unit: "bytes", Timestamp: timeCollected, Collector: "disk"},
			MetricSample{Name: "disk." + name + ".free", Value: float64(free), Unit: "bytes", Timestamp: timeCollected, Collector: "disk"},
			MetricSample{Name: "disk." + name + ".used_percent", Value: usedPercent, Unit: "percent", Timestamp: timeCollected, Collector: "disk"},
		)
	}

	return metricsCollected, nil
}

func mountPointToName(mp string) string {
	if mp == "/" {
		return "root"
	}
	return strings.ReplaceAll(strings.TrimPrefix(mp, "/"), "/", "_")
}

func main() {
	ctx := context.Background()

	cli, err := client.New(client.FromEnv)
	if err != nil {
		fmt.Printf("failed to create docker client: %v\n", err)
		os.Exit(1)
	}
	defer cli.Close()

	meminfoPath := os.Getenv("MEMINFO_PATH")
	if meminfoPath == "" {
		meminfoPath = "meminfo.txt"
	}

	diskInfoPath := os.Getenv("DISKINFO_PATH")
	if diskInfoPath == "" {
		diskInfoPath = "mounts.txt"
	}

	targetMounts := []string{"/", "/home", "/var", "/boot"}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

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

	fmt.Println("Starting monitor (Ctrl+C to stop)")

	go dockerCollector(cli, ctx)

	for range ticker.C {
		fmt.Println("Running memory collection")
		data, err := memoryCollector(meminfoPath)
		if err != nil {
			fmt.Printf("Error collecting memory info: %v\n", err)
		} else {
			for _, d := range data {
				incomingSamplesChannel <- d
			}
		}

		fmt.Println("Running disk collection")
		diskData, err := diskCollector(diskInfoPath, targetMounts)
		if err != nil {
			fmt.Printf("Error collecting disk info: %v\n", err)
		} else {
			for _, d := range diskData {
				incomingSamplesChannel <- d
			}
		}
	}
}
