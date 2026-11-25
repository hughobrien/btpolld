// Package metric is one named value from a device, before it gets labels.
package metric

// Metric is a gauge sample: the Prometheus metric name and its value.
type Metric struct {
	Name  string
	Value float64
}
