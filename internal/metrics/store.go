package metrics

import (
	"slices"
	"sync"
	"time"
)

// Point is a single metric value inside a Sample.
type Point struct {
	Key   Key
	Value float64
}

// Sample is the result of one collection run. Numeric values go into Points;
// structured snapshots (process lists, mounts, interfaces, ...) go into Tables
// keyed by table name.
type Sample struct {
	Time   time.Time
	Points []Point
	Tables map[string]any
}

// Add appends a point to the sample.
func (s *Sample) Add(k Key, v float64) {
	s.Points = append(s.Points, Point{Key: k, Value: v})
}

// SetTable stores a structured snapshot in the sample.
func (s *Sample) SetTable(name string, v any) {
	if s.Tables == nil {
		s.Tables = make(map[string]any)
	}
	s.Tables[name] = v
}

// CapacityRule overrides the ring capacity for series matching Selector.
type CapacityRule struct {
	Selector Selector
	Capacity int
}

// StoreOptions configures a Store.
type StoreOptions struct {
	// Capacity is the default number of samples kept per series.
	Capacity int
	// CapacityRules override Capacity for matching series (first match wins),
	// e.g. a small capacity for per-process series.
	CapacityRules []CapacityRule
}

// DefaultStoreOptions returns sensible defaults.
func DefaultStoreOptions() StoreOptions {
	return StoreOptions{
		Capacity: 600,
		CapacityRules: []CapacityRule{
			{Selector: MustSelector("proc.**"), Capacity: 120},
		},
	}
}

type series struct {
	ring    *Ring
	updated time.Time
}

type tableEntry struct {
	value   any
	updated time.Time
}

type selCacheEntry struct {
	gen  uint64
	keys []Key
}

// Store holds the most recent history of every metric series together with
// the latest table snapshots. It is safe for concurrent use: collectors write
// from their goroutines while the UI reads.
type Store struct {
	opts StoreOptions

	mu       sync.RWMutex
	series   map[Key]*series
	tables   map[string]tableEntry
	gen      uint64 // bumped whenever the set of keys changes
	version  uint64 // bumped on every write
	selCache map[string]selCacheEntry

	subMu  sync.Mutex
	subs   map[int]chan struct{}
	nextID int
}

// NewStore creates an empty Store.
func NewStore(opts StoreOptions) *Store {
	if opts.Capacity <= 0 {
		opts.Capacity = DefaultStoreOptions().Capacity
	}
	return &Store{
		opts:     opts,
		series:   make(map[Key]*series),
		tables:   make(map[string]tableEntry),
		selCache: make(map[string]selCacheEntry),
		subs:     make(map[int]chan struct{}),
	}
}

func (s *Store) capacityFor(k Key) int {
	for _, r := range s.opts.CapacityRules {
		if r.Selector.Match(k) {
			return r.Capacity
		}
	}
	return s.opts.Capacity
}

// Write records a sample and notifies subscribers.
func (s *Store) Write(sm Sample) {
	t := sm.Time
	if t.IsZero() {
		t = time.Now()
	}
	s.mu.Lock()
	for _, p := range sm.Points {
		se, ok := s.series[p.Key]
		if !ok {
			se = &series{ring: NewRing(s.capacityFor(p.Key))}
			s.series[p.Key] = se
			s.gen++
		}
		se.ring.Push(t, p.Value)
		se.updated = t
	}
	for name, v := range sm.Tables {
		s.tables[name] = tableEntry{value: v, updated: t}
	}
	s.version++
	s.mu.Unlock()
	s.notify()
}

// Prune removes series not updated since cutoff (series of exited processes,
// unplugged interfaces, ...) and returns how many were removed.
func (s *Store) Prune(cutoff time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for k, se := range s.series {
		if se.updated.Before(cutoff) {
			delete(s.series, k)
			removed++
		}
	}
	if removed > 0 {
		s.gen++
		clear(s.selCache)
	}
	return removed
}

// Version returns a counter that increases on every write.
func (s *Store) Version() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

// Has reports whether series k exists.
func (s *Store) Has(k Key) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.series[k]
	return ok
}

// Last returns the newest value of k.
func (s *Store) Last(k Key) (float64, bool) {
	_, v, ok := s.LastAt(k)
	return v, ok
}

// LastAt returns the newest sample of k with its timestamp.
func (s *Store) LastAt(k Key) (time.Time, float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	se, ok := s.series[k]
	if !ok {
		return time.Time{}, 0, false
	}
	return se.ring.Last()
}

// Series appends the newest n values of k (oldest first) to dst. n <= 0
// means all stored values.
func (s *Store) Series(k Key, dst []float64, n int) []float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if se, ok := s.series[k]; ok {
		return se.ring.Values(dst, n)
	}
	return dst
}

// Since appends values of k recorded at or after t (oldest first) to dst.
func (s *Store) Since(k Key, dst []float64, t time.Time) []float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if se, ok := s.series[k]; ok {
		return se.ring.Since(dst, t)
	}
	return dst
}

// Keys returns the keys matching sel in natural order (see CompareKeys).
// The returned slice is shared and must not be modified.
func (s *Store) Keys(sel Selector) []Key {
	if sel.IsLiteral() {
		k := sel.Key()
		if s.Has(k) {
			return []Key{k}
		}
		return nil
	}
	s.mu.RLock()
	e, ok := s.selCache[sel.String()]
	gen := s.gen
	s.mu.RUnlock()
	if ok && e.gen == gen {
		return e.keys
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []Key
	for k := range s.series {
		if sel.Match(k) {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, CompareKeys)
	s.selCache[sel.String()] = selCacheEntry{gen: s.gen, keys: keys}
	return keys
}

// Table returns the latest snapshot stored under name and when it was written.
func (s *Store) Table(name string) (any, time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.tables[name]
	return e.value, e.updated, ok
}

// TableAs returns the latest snapshot stored under name as type T. Tables are
// treated as immutable once written, so the value can be read without locks.
func TableAs[T any](s *Store, name string) (T, bool) {
	v, _, ok := s.Table(name)
	if !ok {
		var zero T
		return zero, false
	}
	t, ok := v.(T)
	return t, ok
}

// Subscribe returns a channel that receives a signal after writes. Signals
// are coalesced: a slow reader sees at most one pending signal. Call the
// returned function to unsubscribe.
func (s *Store) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.subMu.Lock()
	id := s.nextID
	s.nextID++
	s.subs[id] = ch
	s.subMu.Unlock()
	return ch, func() {
		s.subMu.Lock()
		delete(s.subs, id)
		s.subMu.Unlock()
	}
}

func (s *Store) notify() {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

