package main

import "time"

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
