package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func processCollector(source string, names []string) []MetricSample {
	samples := make([]MetricSample, 0, len(names))
	if !hasProcessChecks(names) {
		return samples
	}

	collectedAt := time.Now()
	running := runningProcesses(source)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		samples = append(samples, stateSample("process", name, ".missing", !running[name], collectedAt))
	}
	return samples
}

func hasProcessChecks(names []string) bool {
	for _, name := range names {
		if strings.TrimSpace(name) != "" {
			return true
		}
	}
	return false
}

func runningProcesses(source string) map[string]bool {
	processes := make(map[string]bool)
	entries, err := os.ReadDir(source)
	if err != nil {
		return processes
	}
	for _, entry := range entries {
		if !entry.IsDir() || !isPID(entry.Name()) {
			continue
		}
		for _, name := range processNames(filepath.Join(source, entry.Name())) {
			processes[name] = true
		}
	}
	return processes
}

func processNames(dir string) []string {
	names := make([]string, 0, 2)
	if data, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil {
		if name := strings.TrimSpace(string(data)); name != "" {
			names = append(names, name)
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
		fields := strings.Split(string(data), "\x00")
		if len(fields) > 0 && fields[0] != "" {
			names = append(names, filepath.Base(fields[0]))
		}
	}
	return names
}

func isPID(value string) bool {
	_, err := strconv.Atoi(value)
	return err == nil
}
