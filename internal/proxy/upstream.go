package proxy

import "sync/atomic"

// upstream is a single backend endpoint plus its live connection counter.
//
// 32/64-bit safety note: we deliberately use the *typed* atomic wrappers
// (atomic.Int64, atomic.Uint64) instead of the raw free functions
// (atomic.AddInt64(&x, 1)). The typed wrappers embed an alignment guarantee,
// so they are correct on 32-bit targets (386, arm) where a plain int64 field
// is only 4-byte aligned — calling a raw 64-bit atomic on such a field would
// panic with "unaligned 64-bit atomic operation".
//
// Cache-line padding is intentionally NOT done yet. Every upstream has its own
// cache line only by luck right now; if profiling shows false sharing between
// hot counters, we will pad `active` to a full line. Deferred on purpose.
type upstream struct {
	addr   string
	active atomic.Int64
}

// Balancer selects the next upstream for a new connection.
//
// This is an extension point: new strategies (weighted RR, hash by client IP,
// least-latency, ...) implement this interface without touching the data path.
type Balancer interface {
	Next(servers []*upstream) *upstream
	Name() string
}

func newBalancer(kind string) Balancer {
	if kind == "least_conn" {
		return &leastConn{}
	}
	return &roundRobin{}
}

// roundRobin cycles through the upstreams.
type roundRobin struct {
	next atomic.Uint64
}

func (b *roundRobin) Name() string { return "round_robin" }

func (b *roundRobin) Next(servers []*upstream) *upstream {
	i := b.next.Add(1) - 1
	return servers[i%uint64(len(servers))]
}

// leastConn picks the upstream with the fewest active connections.
type leastConn struct{}

func (b *leastConn) Name() string { return "least_conn" }

func (b *leastConn) Next(servers []*upstream) *upstream {
	best := servers[0]
	bestActive := best.active.Load()
	for _, s := range servers[1:] {
		if a := s.active.Load(); a < bestActive {
			best, bestActive = s, a
		}
	}
	return best
}
