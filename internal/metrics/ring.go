package metrics

import "time"

// Ring is a bounded circular buffer of timestamped float64 samples. Storage
// grows on demand up to the capacity and is then reused, so short-lived series
// stay small and Push does not allocate once the ring is full. Ring is not
// safe for concurrent use; Store guards it.
type Ring struct {
	ts   []int64 // unix milliseconds
	vals []float64
	max  int
	head int // index of the next write once full; equals len(vals) while growing
	n    int
}

const ringInitialCap = 64

// NewRing creates a ring holding up to capacity samples.
func NewRing(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	c := min(capacity, ringInitialCap)
	return &Ring{ts: make([]int64, 0, c), vals: make([]float64, 0, c), max: capacity}
}

// Cap returns the capacity of the ring.
func (r *Ring) Cap() int { return r.max }

// Len returns the number of stored samples.
func (r *Ring) Len() int { return r.n }

// Push appends a sample, overwriting the oldest one when full.
func (r *Ring) Push(t time.Time, v float64) {
	if len(r.vals) < r.max {
		r.ts = append(r.ts, t.UnixMilli())
		r.vals = append(r.vals, v)
		r.n = len(r.vals)
		r.head = r.n % r.max
		return
	}
	r.ts[r.head] = t.UnixMilli()
	r.vals[r.head] = v
	r.head = (r.head + 1) % r.max
}

// idx maps a logical index (0 = oldest) to a physical index.
func (r *Ring) idx(i int) int {
	return (r.head - r.n + i + len(r.vals)) % len(r.vals)
}

// At returns the i-th sample, where 0 is the oldest. It panics if i is out of range.
func (r *Ring) At(i int) (time.Time, float64) {
	if i < 0 || i >= r.n {
		panic("metrics: ring index out of range")
	}
	j := r.idx(i)
	return time.UnixMilli(r.ts[j]), r.vals[j]
}

// Last returns the newest sample and false if the ring is empty.
func (r *Ring) Last() (time.Time, float64, bool) {
	if r.n == 0 {
		return time.Time{}, 0, false
	}
	t, v := r.At(r.n - 1)
	return t, v, true
}

// Values appends the newest n values (oldest first) to dst and returns it.
// If n <= 0 or n exceeds Len, all values are returned.
func (r *Ring) Values(dst []float64, n int) []float64 {
	if n <= 0 || n > r.n {
		n = r.n
	}
	for i := r.n - n; i < r.n; i++ {
		dst = append(dst, r.vals[r.idx(i)])
	}
	return dst
}

// Since appends values with timestamps >= t (oldest first) to dst.
func (r *Ring) Since(dst []float64, t time.Time) []float64 {
	ms := t.UnixMilli()
	start := r.n
	for start > 0 && r.ts[r.idx(start-1)] >= ms {
		start--
	}
	for i := start; i < r.n; i++ {
		dst = append(dst, r.vals[r.idx(i)])
	}
	return dst
}

// Reset removes all samples.
func (r *Ring) Reset() {
	r.ts, r.vals = r.ts[:0], r.vals[:0]
	r.head, r.n = 0, 0
}
