package proxy

// Pool groups upstreams under a single balancing strategy.
//
// A Pool is (re)built from a configuration snapshot. Its balancing state is
// mutable, but because callers hold the exact *upstream they reserved, a pool
// rebuilt during a reload can never decrement the wrong counter — the old
// *upstream simply becomes garbage once its connections drain.
//
// Note: after a reload the new pool starts with fresh counters (active == 0),
// so connections still running on the old pool are not counted by the new one.
// For least_conn this causes a short-lived imbalance until the old connections
// drain; for round_robin it does not matter.
type Pool struct {
	name     string      // pool name from the config (used in logs)
	servers  []*upstream // upstreams in config order; balancers rely on this order
	balancer Balancer    // strategy chosen from PoolConfig.Balance
}

// newPool constructs a pool together with its balancer. balance is the raw
// config string; newBalancer maps it to a strategy.
//
// Precondition: servers must be non-empty. Config validation guarantees this,
// and an empty slice here would make balancer selection panic, so we fail fast
// on a programming error.
func newPool(name string, servers []string, balance string) *Pool {
	if len(servers) == 0 {
		panic("proxy: newPool called with an empty server list")
	}
	ups := make([]*upstream, 0, len(servers))
	for _, s := range servers {
		ups = append(ups, &upstream{addr: s})
	}
	return &Pool{name: name, servers: ups, balancer: newBalancer(balance)}
}

// Next reserves the chosen upstream. The caller MUST call Release with the
// returned pointer exactly once when the connection is done.
func (p *Pool) Next() *upstream {
	u := p.balancer.Next(p.servers)
	u.active.Add(1)
	return u
}

// Release returns a reserved slot. Safe to call with nil.
//
// The decrement is guarded so a stray double-Release cannot drive the counter
// negative: a negative active count would make least_conn consider the upstream
// "extra free" and funnel ever more connections to it.
func (p *Pool) Release(u *upstream) {
	if u == nil {
		return
	}
	for {
		cur := u.active.Load()
		if cur <= 0 {
			return // nothing to release (defensive)
		}
		if u.active.CompareAndSwap(cur, cur-1) {
			return
		}
	}
}

// Name returns the pool name (used in logs).
func (p *Pool) Name() string { return p.name }
