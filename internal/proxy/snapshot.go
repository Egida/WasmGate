package proxy

import (
	"sync"
	"sync/atomic"

	"wasmgate/internal/config"
)

// resolvedListener is a listener pre-bound to its pool, so the data path
// resolves everything it needs with a single map lookup per connection instead
// of scanning the listener list and then looking up the pool.
type resolvedListener struct {
	cfg  config.ListenerConfig
	pool *Pool
	// sem enforces cfg.MaxConns when it is > 0; nil means "unlimited".
	sem chan struct{}
}

// Snapshot is an immutable, ready-to-use view of the configuration together
// with the runtime objects compiled from it.
//
// "Immutable" means its structure never changes after it is published. The
// pools inside carry live counters, but the map and the pool set are frozen —
// this is what makes lock-free reads safe.
type Snapshot struct {
	Version   uint64
	Config    *config.Config
	listeners map[string]resolvedListener
}

// Runtime publishes Snapshots atomically.
//
// This is the core seam for Zero-Downtime Hot Reload:
//   - the data path calls Load() on every connection — a single atomic load,
//     no locks, no torn reads;
//   - the control plane (or SIGHUP handler) calls Reload() to publish a new
//     immutable snapshot.
//
// Already-running connections keep using the snapshot they captured, so
// replacing the configuration never interrupts them.
type Runtime struct {
	cur atomic.Pointer[Snapshot]
	// mu serializes Reload. The pointer swap itself is atomic, but computing the
	// next version is a read-modify-write: without this lock two concurrent
	// reloads could publish the same version (a lost update).
	mu sync.Mutex
}

// NewRuntime builds the first snapshot (version 1) from cfg.
func NewRuntime(cfg *config.Config) *Runtime {
	r := &Runtime{}
	r.cur.Store(buildSnapshot(1, cfg))
	return r
}

// Load returns the current snapshot. Lock-free and allocation-free.
func (r *Runtime) Load() *Snapshot { return r.cur.Load() }

// Reload compiles a new snapshot and publishes it, returning the new version.
// Safe for concurrent use.
func (r *Runtime) Reload(cfg *config.Config) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	next := buildSnapshot(r.cur.Load().Version+1, cfg)
	r.cur.Store(next)
	return next.Version
}

// buildSnapshot compiles cfg into a ready-to-use Snapshot: it turns every
// configured pool into a live Pool (upstreams + balancer), pre-resolves every
// listener to its pool (and its connection limit), and tags the result with the
// given version. All allocation happens here, up front, so the data path only
// ever reads.
func buildSnapshot(version uint64, cfg *config.Config) *Snapshot {
	pools := make(map[string]*Pool, len(cfg.Pools))
	for name, pc := range cfg.Pools {
		pools[name] = newPool(name, pc.Servers, pc.Balance)
	}

	listeners := make(map[string]resolvedListener, len(cfg.Listeners))
	for _, lc := range cfg.Listeners {
		rl := resolvedListener{cfg: lc, pool: pools[lc.UpstreamPool]}
		if lc.MaxConns > 0 {
			rl.sem = make(chan struct{}, lc.MaxConns)
		}
		listeners[lc.Name] = rl
	}

	return &Snapshot{Version: version, Config: cfg, listeners: listeners}
}
