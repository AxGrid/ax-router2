package server

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	latencyRingSize = 256
	rateRingSize    = 60 // last 60 seconds
)

// Stats holds per-service counters and rolling samples. One Stats outlives
// individual yamux sessions: a service that connects → disconnects → reconnects
// keeps cumulative totals, with Connected toggled.
type Stats struct {
	Service string

	// Connection state (mutex-protected).
	mu             sync.Mutex
	connected      bool
	remote         string
	connectedAt    time.Time
	disconnectedAt time.Time

	// Atomic cumulative counters.
	httpRequests atomic.Uint64
	httpBytesIn  atomic.Uint64
	httpBytesOut atomic.Uint64
	httpActive   atomic.Int64

	wsUpgrades atomic.Uint64
	wsBytesIn  atomic.Uint64
	wsBytesOut atomic.Uint64
	wsActive   atomic.Int64

	// Latency samples in microseconds (mu-protected).
	latencyRing [latencyRingSize]uint32
	latencyHead int
	latencyN    int // count up to ring size

	// 60-second rolling buckets (mu-protected). bucketUnix is the unix-second
	// of buckets[head].
	rpsBuckets [rateRingSize]uint32
	bpsBuckets [rateRingSize]uint64 // bytes in+out
	bucketUnix int64
}

// StatsSnapshot is the JSON-friendly view of Stats sent to the dashboard.
type StatsSnapshot struct {
	Service        string   `json:"service"`
	Connected      bool     `json:"connected"`
	Remote         string   `json:"remote,omitempty"`
	ConnectedAtMs  int64    `json:"connectedAtMs,omitempty"`
	DisconnectedAt int64    `json:"disconnectedAtMs,omitempty"`
	HTTPRequests   uint64   `json:"httpRequests"`
	HTTPBytesIn    uint64   `json:"httpBytesIn"`
	HTTPBytesOut   uint64   `json:"httpBytesOut"`
	HTTPActive     int64    `json:"httpActive"`
	WSUpgrades     uint64   `json:"wsUpgrades"`
	WSBytesIn      uint64   `json:"wsBytesIn"`
	WSBytesOut     uint64   `json:"wsBytesOut"`
	WSActive       int64    `json:"wsActive"`
	LatencyP50Us   uint32   `json:"latencyP50Us"`
	LatencyP95Us   uint32   `json:"latencyP95Us"`
	LatencyP99Us   uint32   `json:"latencyP99Us"`
	RPS60          []uint32 `json:"rps60"` // last 60 seconds, oldest first
	BPS60          []uint64 `json:"bps60"`
}

func (s *Stats) MarkConnected(remote string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = true
	s.remote = remote
	s.connectedAt = time.Now()
	s.disconnectedAt = time.Time{}
}

func (s *Stats) MarkDisconnected() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = false
	s.disconnectedAt = time.Now()
}

func (s *Stats) HTTPStreamStart() { s.httpActive.Add(1) }
func (s *Stats) HTTPStreamEnd()   { s.httpActive.Add(-1) }

func (s *Stats) WSStart() {
	s.wsActive.Add(1)
	s.wsUpgrades.Add(1)
}
func (s *Stats) WSEnd() { s.wsActive.Add(-1) }

func (s *Stats) AddHTTPBytes(in, out uint64) {
	if in > 0 {
		s.httpBytesIn.Add(in)
	}
	if out > 0 {
		s.httpBytesOut.Add(out)
	}
	s.bumpBucket(0, in+out)
}

func (s *Stats) AddWSBytes(in, out uint64) {
	if in > 0 {
		s.wsBytesIn.Add(in)
	}
	if out > 0 {
		s.wsBytesOut.Add(out)
	}
	s.bumpBucket(0, in+out)
}

// HTTPCompleted records one finished HTTP request: latency, byte counts.
func (s *Stats) HTTPCompleted(latency time.Duration, bytesIn, bytesOut uint64) {
	s.httpRequests.Add(1)
	s.AddHTTPBytes(bytesIn, bytesOut)

	usec := latency.Microseconds()
	if usec < 0 {
		usec = 0
	}
	if usec > 0xffffffff {
		usec = 0xffffffff
	}

	s.mu.Lock()
	s.latencyRing[s.latencyHead] = uint32(usec)
	s.latencyHead = (s.latencyHead + 1) % latencyRingSize
	if s.latencyN < latencyRingSize {
		s.latencyN++
	}
	s.mu.Unlock()

	s.bumpBucket(1, 0)
}

// bumpBucket advances the second-aligned ring (if needed) and adds counts.
// requests is a counter (typically 0 or 1), bytes is total bytes added.
func (s *Stats) bumpBucket(requests uint32, bytes uint64) {
	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advanceBucketsLocked(now)
	idx := int(now % rateRingSize)
	s.rpsBuckets[idx] += requests
	s.bpsBuckets[idx] += bytes
}

// advanceBucketsLocked zeroes any bucket whose timestamp is now stale.
func (s *Stats) advanceBucketsLocked(now int64) {
	if s.bucketUnix == 0 {
		s.bucketUnix = now
		return
	}
	gap := now - s.bucketUnix
	if gap <= 0 {
		return
	}
	if gap >= rateRingSize {
		// Whole ring aged out.
		for i := range s.rpsBuckets {
			s.rpsBuckets[i] = 0
			s.bpsBuckets[i] = 0
		}
	} else {
		// Zero buckets in (s.bucketUnix, now].
		for k := int64(1); k <= gap; k++ {
			idx := int((s.bucketUnix + k) % rateRingSize)
			s.rpsBuckets[idx] = 0
			s.bpsBuckets[idx] = 0
		}
	}
	s.bucketUnix = now
}

// Snapshot returns a copy safe for serialization.
func (s *Stats) Snapshot() StatsSnapshot {
	now := time.Now().Unix()

	s.mu.Lock()
	s.advanceBucketsLocked(now)
	rps := make([]uint32, rateRingSize)
	bps := make([]uint64, rateRingSize)
	// Emit oldest-first relative to "now".
	for i := 0; i < rateRingSize; i++ {
		idx := int((now - int64(rateRingSize-1) + int64(i)) % rateRingSize)
		if idx < 0 {
			idx += rateRingSize
		}
		rps[i] = s.rpsBuckets[idx]
		bps[i] = s.bpsBuckets[idx]
	}
	// Note: the head bucket (i.e. current second) is included as the last
	// element; older buckets to the left.

	// Latency percentiles.
	samples := make([]uint32, s.latencyN)
	if s.latencyN < latencyRingSize {
		copy(samples, s.latencyRing[:s.latencyN])
	} else {
		copy(samples, s.latencyRing[:])
	}
	connected := s.connected
	remote := s.remote
	connectedAt := s.connectedAt
	disconnectedAt := s.disconnectedAt
	s.mu.Unlock()

	p50, p95, p99 := percentiles(samples)

	snap := StatsSnapshot{
		Service:      s.Service,
		Connected:    connected,
		Remote:       remote,
		HTTPRequests: s.httpRequests.Load(),
		HTTPBytesIn:  s.httpBytesIn.Load(),
		HTTPBytesOut: s.httpBytesOut.Load(),
		HTTPActive:   s.httpActive.Load(),
		WSUpgrades:   s.wsUpgrades.Load(),
		WSBytesIn:    s.wsBytesIn.Load(),
		WSBytesOut:   s.wsBytesOut.Load(),
		WSActive:     s.wsActive.Load(),
		LatencyP50Us: p50,
		LatencyP95Us: p95,
		LatencyP99Us: p99,
		RPS60:        rps,
		BPS60:        bps,
	}
	if !connectedAt.IsZero() {
		snap.ConnectedAtMs = connectedAt.UnixMilli()
	}
	if !disconnectedAt.IsZero() {
		snap.DisconnectedAt = disconnectedAt.UnixMilli()
	}
	return snap
}

func percentiles(samples []uint32) (p50, p95, p99 uint32) {
	if len(samples) == 0 {
		return 0, 0, 0
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	pick := func(p float64) uint32 {
		idx := int(float64(len(samples)-1) * p)
		return samples[idx]
	}
	return pick(0.50), pick(0.95), pick(0.99)
}

// statsRegistry maps service name → *Stats. Stats outlive sessions.
type statsRegistry struct {
	mu sync.Mutex
	m  map[string]*Stats
}

func newStatsRegistry() *statsRegistry {
	return &statsRegistry{m: map[string]*Stats{}}
}

// Get returns the Stats for service, creating it if absent.
func (r *statsRegistry) Get(service string) *Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.m[service]
	if !ok {
		s = &Stats{Service: service}
		r.m[service] = s
	}
	return s
}

// SnapshotAll returns a deterministic, sorted slice of snapshots.
func (r *statsRegistry) SnapshotAll() []StatsSnapshot {
	r.mu.Lock()
	names := make([]string, 0, len(r.m))
	stats := make([]*Stats, 0, len(r.m))
	for name, s := range r.m {
		names = append(names, name)
		stats = append(stats, s)
	}
	r.mu.Unlock()

	idx := make([]int, len(names))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(i, j int) bool { return names[idx[i]] < names[idx[j]] })
	out := make([]StatsSnapshot, len(names))
	for i, j := range idx {
		out[i] = stats[j].Snapshot()
	}
	return out
}
