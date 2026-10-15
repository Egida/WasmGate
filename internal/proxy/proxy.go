package proxy

import (
	"context"
	"log/slog"
	"net"
)

// Proxy is the data-path handler: it turns an accepted client connection into a
// proxied session against an upstream.
//
// Extension points (added in later milestones, kept as explicit seams):
//   - DDoS / rate limiting on the sliding window: checked right after Resolve,
//     before dialing the upstream, so abusive connections are dropped cheaply.
//   - WASM plugin hooks: on_accept / on_data / on_close, invoked around the
//     data loop.
type Proxy struct {
	log *slog.Logger // structured logger
	rt  *Runtime     // source of the current snapshot
}

// New returns a Proxy that reads configuration from rt on every connection.
func New(log *slog.Logger, rt *Runtime) *Proxy {
	return &Proxy{log: log, rt: rt}
}

// Handle proxies a single accepted client connection. It always closes client.
//
// The current snapshot is captured once and used for the whole lifetime of the
// connection, so a configuration reload mid-connection cannot affect it.
func (p *Proxy) Handle(ctx context.Context, listenerName string, client net.Conn) {
	defer client.Close()

	snap := p.rt.Load()

	lc, ok := snap.Config.Listener(listenerName)
	if !ok {
		p.log.Error("listener missing from snapshot", "listener", listenerName)
		return
	}
	pool := snap.Pools[lc.UpstreamPool]
	if pool == nil {
		p.log.Error("pool missing from snapshot", "listener", listenerName, "pool", lc.UpstreamPool)
		return
	}

	// --- FUTURE: DDoS / sliding-window rate limit check goes HERE. ---

	up := pool.Next()
	defer pool.Release(up)

	dialCtx, cancel := context.WithTimeout(ctx, lc.DialTimeout.Std())
	defer cancel()

	var d net.Dialer
	server, err := d.DialContext(dialCtx, "tcp", up.addr)
	if err != nil {
		p.log.Warn("dial upstream failed", "listener", listenerName, "upstream", up.addr, "err", err)
		return
	}
	defer server.Close()

	p.log.Debug("proxying", "listener", listenerName, "upstream", up.addr)

	join(client, server)
}
