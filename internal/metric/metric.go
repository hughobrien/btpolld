// Package metric is one named value from a device, before it gets labels.
package metric

import "strings"

// Metric is a gauge sample: the Prometheus metric name and its value.
type Metric struct {
	Name  string
	Value float64
}

// Rename replaces the leading prefix_ of each name with to_, returning the
// result (the input is left alone). An empty to, or one equal to prefix,
// changes nothing; a name without the prefix is kept as it is.
func Rename(ms []Metric, prefix, to string) []Metric {
	if to == "" || to == prefix {
		return ms
	}
	out := make([]Metric, len(ms))
	for i, m := range ms {
		if rest, ok := strings.CutPrefix(m.Name, prefix+"_"); ok {
			m.Name = to + "_" + rest
		}
		out[i] = m
	}
	return out
}
