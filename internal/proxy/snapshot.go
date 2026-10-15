package proxy

import (
	"sync/atomic"

	"wasmgate/internal/config"
)

// Snapshot is an immutable, ready-to-use view of the configuration together
// with the runtime Pools compiled from it.
//
// "Immutable" means its structure never changes after it is published. The
// Pools inside carry live counters, but the map and the pool set are frozen —
// this is what makes lock-free reads safe.
type Snapshot struct {
	Version uint64
	Config  *config.Config
	Pools   map[string]*Pool
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
func (r *Runtime) Reload(cfg *config.Config) uint64 {
	next := buildSnapshot(r.cur.Load().Version+1, cfg)
	r.cur.Store(next)
	return next.Version
}

func buildSnapshot(version uint64, cfg *config.Config) *Snapshot {
	pools := make(map[string]*Pool, len(cfg.Pools))
	for name, pc := range cfg.Pools {
		pools[name] = newPool(name, pc.Servers, pc.Balance)
	}
	return &Snapshot{Version: version, Config: cfg, Pools: pools}
}
