package chart

import "math"

// Tail returns the last n values of v (all of v if it is shorter).
func Tail(v []float64, n int) []float64 {
	if n < 0 {
		n = 0
	}
	if len(v) > n {
		return v[len(v)-n:]
	}
	return v
}

// Agg combines a bucket of values into one.
type Agg uint8

// Aggregations.
const (
	AggMax Agg = iota
	AggAvg
	AggMin
	AggLast
)

// Resample reduces v to at most n points by aggregating consecutive buckets.
// The newest value always ends up in the last bucket. If v already fits it is
// returned unchanged. The result is appended to dst.
func Resample(dst, v []float64, n int, agg Agg) []float64 {
	if n <= 0 {
		return dst
	}
	if len(v) <= n {
		return append(dst, v...)
	}
	// Bucket boundaries are aligned to the end so the right edge is stable as
	// new samples arrive.
	size := float64(len(v)) / float64(n)
	for i := 0; i < n; i++ {
		start := len(v) - int(math.Round(float64(n-i)*size))
		end := len(v) - int(math.Round(float64(n-i-1)*size))
		start, end = max(start, 0), max(end, start+1)
		dst = append(dst, aggregate(v[start:min(end, len(v))], agg))
	}
	return dst
}

func aggregate(b []float64, agg Agg) float64 {
	if len(b) == 0 {
		return 0
	}
	switch agg {
	case AggAvg:
		var s float64
		for _, x := range b {
			s += x
		}
		return s / float64(len(b))
	case AggMin:
		m := b[0]
		for _, x := range b[1:] {
			m = min(m, x)
		}
		return m
	case AggLast:
		return b[len(b)-1]
	default:
		m := b[0]
		for _, x := range b[1:] {
			m = max(m, x)
		}
		return m
	}
}
