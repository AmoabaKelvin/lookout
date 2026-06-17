package main

import (
	"strings"
	"time"
	"unicode"
)

type MetricSample struct {
	Name      string
	Value     float64
	Unit      string
	Timestamp time.Time
	Collector string
}

// stateSample builds a 0/1 health gauge for a named target (systemd unit, TCP/HTTP
// check, process). Value is 1 when unhealthy/missing, 0 otherwise.
func stateSample(collector, name, suffix string, unhealthy bool, at time.Time) MetricSample {
	var value float64
	if unhealthy {
		value = 1
	}
	return MetricSample{
		Name:      collector + "." + safeMetricPart(name) + suffix,
		Value:     value,
		Unit:      "state",
		Timestamp: at,
		Collector: collector,
	}
}

func safeMetricPart(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}
