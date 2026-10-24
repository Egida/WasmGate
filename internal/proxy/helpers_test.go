package proxy

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"wasmgate/internal/config"
)

// Fixtures shared by the tests and benchmarks in this package.
//
// Layout rule: tests and benchmarks live in the file named after the source file
// they cover (server.go -> server_test.go, copy.go -> copy_test.go and so on),
// tests first, benchmarks below. This file holds only what more than one of them
// needs.

// Sinks stop the compiler from optimising the benchmarked calls away.
var (
	sinkUpstream *upstream
	sinkListener resolvedListener
	sinkSnapshot *Snapshot
	sinkVersion  uint64
)

// testLogger returns a logger that discards everything, so test output stays
// readable.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startEcho starts a trivial TCP echo server and returns its address; the
// listener is closed when tb's cleanup runs.
func startEcho(tb testing.TB) string {
	tb.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("listen echo: %v", err)
	}
	tb.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go echo(c)
		}
	}()
	return ln.Addr().String()
}

// echo copies everything it reads back to the client until the peer goes away.
// It uses an explicit read/write loop rather than io.Copy so that splicing a
// socket onto itself can never be attempted.
func echo(c net.Conn) {
	defer c.Close()
	buf := make([]byte, 32<<10)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			if _, werr := c.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// dial opens a client connection to addr, failing tb on error.
func dial(tb testing.TB, addr string) net.Conn {
	tb.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		tb.Fatalf("dial %s: %v", addr, err)
	}
	return conn
}

// edgeConfig builds a config with a single listener "edge" feeding pool "web",
// which holds backends. The listener gets a 1s dial timeout; mutate may adjust it
// further (nil is fine).
func edgeConfig(backends []string, mutate func(*config.ListenerConfig)) *config.Config {
	lc := config.ListenerConfig{
		Name:         "edge",
		Addr:         "127.0.0.1:0",
		UpstreamPool: "web",
		DialTimeout:  time.Second,
	}
	if mutate != nil {
		mutate(&lc)
	}
	return &config.Config{
		Version:   1,
		Listeners: []config.ListenerConfig{lc},
		Pools:     map[string]config.PoolConfig{"web": {Servers: backends}},
	}
}

// validated returns cfg after checking it, failing tb if the config is invalid.
func validated(tb testing.TB, cfg *config.Config) *config.Config {
	tb.Helper()
	if err := cfg.Validate(); err != nil {
		tb.Fatalf("validate: %v", err)
	}
	return cfg
}

// startServer starts srv and registers its shutdown with tb's cleanup.
func startServer(tb testing.TB, srv *Server) string {
	tb.Helper()
	if err := srv.Start(); err != nil {
		tb.Fatalf("start: %v", err)
	}
	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return srv.Addrs()[0].String()
}

// newTestServer starts a proxy in front of an echo server and returns the
// proxy's listen address plus the server handle.
func newTestServer(t *testing.T, mutate func(*config.ListenerConfig)) (string, *Server) {
	t.Helper()
	srv := NewServer(testLogger(), NewRuntime(edgeConfig([]string{startEcho(t)}, mutate)), nil)
	return startServer(t, srv), srv
}

// startProxy starts a Server with one listener feeding pool "web" (backend) and
// returns its address. idle is the listener idle_timeout (0 keeps the zero-copy
// path).
func startProxy(tb testing.TB, backend string, idle time.Duration) string {
	tb.Helper()
	cfg := validated(tb, edgeConfig([]string{backend}, func(lc *config.ListenerConfig) {
		lc.IdleTimeout = idle
	}))
	return startServer(tb, NewServer(testLogger(), NewRuntime(cfg), nil))
}

// waitMetric polls cond until it reports true or the deadline expires, failing
// the test on timeout. Metrics are updated from the connection goroutines, so a
// test that just closed its socket must give them a moment to land.
func waitMetric(t *testing.T, deadline time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(deadline)
	for !cond() {
		if time.Now().After(end) {
			t.Fatal("metrics did not settle in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// roundTrip opens a connection, sends one byte and waits for the echoed byte.
func roundTrip(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn := dial(t, addr)
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	return conn
}
