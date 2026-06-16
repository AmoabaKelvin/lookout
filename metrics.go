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

func safeMetricPart(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '_' || r == '-':
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
