package proxy

// Pool groups upstreams under a single balancing strategy.
//
// A Pool is (re)built from a configuration snapshot. Its balancing state is
// mutable, but because callers hold the exact *upstream they reserved, a pool
// rebuilt during a reload can never decrement the wrong counter — the old
// *upstream simply becomes garbage once its connections drain.
type Pool struct {
	name     string      // pool name from the config (used in logs/metrics)
	servers  []*upstream // upstreams in config order; balancers rely on this order
	balancer Balancer    // strategy chosen from PoolConfig.Balance
}

// newPool constructs a pool together with its balancer. balance is the raw
// config string; newBalancer maps it to a strategy. Unknown values fall back to
// round-robin here, but validation already rejects them before this point.
func newPool(name string, servers []string, balance string) *Pool {
	ups := make([]*upstream, 0, len(servers))
	for _, s := range servers {
		ups = append(ups, &upstream{addr: s})
	}
	return &Pool{name: name, servers: ups, balancer: newBalancer(balance)}
}

// Next reserves the chosen upstream. The caller MUST call Release with the
// returned pointer when the connection is done.
func (p *Pool) Next() *upstream {
	u := p.balancer.Next(p.servers)
	u.active.Add(1)
	return u
}

// Release returns a reserved slot. Safe to call with nil.
func (p *Pool) Release(u *upstream) {
	if u != nil {
		u.active.Add(-1)
	}
}

// Name returns the pool name. Currently informational (logs/metrics); reserved
// for the observability milestone.
func (p *Pool) Name() string { return p.name }
