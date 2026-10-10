package metrics

import "sync"

// Unit describes how a metric value should be interpreted and formatted.
type Unit uint8

// Supported units.
const (
	UnitNone        Unit = iota
	UnitPercent          // 0..100
	UnitBytes            // bytes
	UnitBytesPerSec      // bytes per second
	UnitCelsius          // degrees Celsius
	UnitHertz            // Hz
	UnitSeconds          // duration in seconds
	UnitCount            // dimensionless count
	UnitPerSec           // events per second
	UnitWatts            // W
	UnitRPM              // revolutions per minute
)

var unitNames = [...]string{"", "percent", "bytes", "bytes/s", "celsius", "hz", "seconds", "count", "1/s", "watts", "rpm"}

func (u Unit) String() string {
	if int(u) < len(unitNames) {
		return unitNames[u]
	}
	return "unknown"
}

const maxRegistryCache = 16384

// Desc describes a family of metrics matched by a selector pattern.
type Desc struct {
	Pattern string
	Unit    Unit
	Help    string
	// Min and Max give the natural range of values; Max == 0 means unbounded
	// (graphs should auto-scale).
	Min, Max float64

	sel Selector
}

// Registry maps metric keys to descriptions. The first registered pattern
// that matches a key wins, so register specific patterns before generic ones.
type Registry struct {
	mu    sync.RWMutex
	descs []Desc
	cache map[Key]*Desc
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{cache: make(map[Key]*Desc)}
}

// Register adds descriptions. It panics on an invalid pattern, since patterns
// are defined in code by collectors.
func (r *Registry) Register(descs ...Desc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range descs {
		d.sel = MustSelector(d.Pattern)
		r.descs = append(r.descs, d)
	}
	clear(r.cache)
}

// Lookup returns the description for k, or nil if none matches.
func (r *Registry) Lookup(k Key) *Desc {
	r.mu.RLock()
	d, ok := r.cache[k]
	r.mu.RUnlock()
	if ok {
		return d
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	d = nil
	for i := range r.descs {
		if r.descs[i].sel.Match(k) {
			d = &r.descs[i]
			break
		}
	}
	if len(r.cache) >= maxRegistryCache { // keys like proc.<pid>.* churn
		clear(r.cache)
	}
	r.cache[k] = d
	return d
}

// UnitOf returns the unit of k, or UnitNone if unknown.
func (r *Registry) UnitOf(k Key) Unit {
	if d := r.Lookup(k); d != nil {
		return d.Unit
	}
	return UnitNone
}

// DefaultRegistry is the process-wide registry collectors register into.
var DefaultRegistry = NewRegistry()
