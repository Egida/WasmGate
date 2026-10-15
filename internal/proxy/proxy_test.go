package proxy

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"wasmgate/internal/config"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startEcho starts a trivial TCP echo server and returns its address.
func startEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = io.Copy(c, c)
				_ = c.Close()
			}()
		}
	}()
	return ln.Addr().String()
}

// newTestServer starts a proxy in front of an echo server and returns the
// proxy's listen address plus the server handle (already registered for cleanup).
func newTestServer(t *testing.T, mutate func(*config.ListenerConfig)) (string, *Server) {
	t.Helper()
	echoAddr := startEcho(t)

	lc := config.ListenerConfig{
		Name:         "edge",
		Addr:         "127.0.0.1:0",
		UpstreamPool: "web",
		DialTimeout:  config.Duration(time.Second),
	}
	if mutate != nil {
		mutate(&lc)
	}

	cfg := &config.Config{
		Version:   1,
		Listeners: []config.ListenerConfig{lc},
		Pools:     map[string]config.PoolConfig{"web": {Servers: []string{echoAddr}}},
	}

	srv := NewServer(testLogger(), NewRuntime(cfg))
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	})
	return srv.Addrs()[0].String(), srv
}

// roundTrip opens a connection, sends one byte and waits for the echoed byte.
func roundTrip(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
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

func TestProxyForwardsBytes(t *testing.T) {
	echoAddr := startEcho(t)

	cfg := &config.Config{
		Version: 1,
		Listeners: []config.ListenerConfig{{
			Name:         "edge",
			Addr:         "127.0.0.1:0",
			UpstreamPool: "web",
			DialTimeout:  config.Duration(time.Second),
		}},
		Pools: map[string]config.PoolConfig{
			"web": {Balance: "round_robin", Servers: []string{echoAddr}},
		},
	}

	rt := NewRuntime(cfg)
	srv := NewServer(testLogger(), rt)

	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	})

	addr := srv.Addrs()[0].String()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()

	const payload = "hello wasmgate"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.(*net.TCPConn).CloseWrite()

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != payload {
		t.Fatalf("got %q, want %q", buf, payload)
	}
}

func TestRoundRobinDistribution(t *testing.T) {
	p := newPool("p", []string{"a", "b", "c"}, "round_robin")
	counts := map[string]int{}
	for i := 0; i < 9; i++ {
		u := p.Next()
		counts[u.addr]++
		p.Release(u)
	}
	for _, a := range []string{"a", "b", "c"} {
		if counts[a] != 3 {
			t.Fatalf("upstream %s got %d picks, want 3", a, counts[a])
		}
	}
}

func TestLeastConnPicksLeastActive(t *testing.T) {
	p := newPool("p", []string{"a", "b"}, "least_conn")
	p.servers[0].active.Store(5)
	p.servers[1].active.Store(1)

	u := p.Next()
	if u.addr != "b" {
		t.Fatalf("least_conn picked %s, want b", u.addr)
	}
}

func TestReloadBumpsVersion(t *testing.T) {
	cfg := &config.Config{
		Version:   1,
		Listeners: []config.ListenerConfig{{Name: "edge", Addr: "127.0.0.1:0", UpstreamPool: "web"}},
		Pools:     map[string]config.PoolConfig{"web": {Servers: []string{"a"}}},
	}
	rt := NewRuntime(cfg)
	if got := rt.Load().Version; got != 1 {
		t.Fatalf("initial version = %d, want 1", got)
	}
	if got := rt.Reload(cfg); got != 2 {
		t.Fatalf("reloaded version = %d, want 2", got)
	}
}

// TestReleaseNeverGoesNegative guards against a stray double-Release driving the
// counter negative (which would break least_conn).
func TestReleaseNeverGoesNegative(t *testing.T) {
	p := newPool("p", []string{"a"}, "least_conn")
	u := p.Next()
	p.Release(u)
	p.Release(u) // second release must be a no-op
	if got := u.active.Load(); got != 0 {
		t.Fatalf("active = %d after double Release, want 0", got)
	}
}

// TestConcurrentReloadVersionsUnique verifies that concurrent reloads never
// publish the same version (the read-modify-write must be serialized).
func TestConcurrentReloadVersionsUnique(t *testing.T) {
	cfg := &config.Config{
		Version:   1,
		Listeners: []config.ListenerConfig{{Name: "edge", Addr: "127.0.0.1:0", UpstreamPool: "web"}},
		Pools:     map[string]config.PoolConfig{"web": {Servers: []string{"a"}}},
	}
	rt := NewRuntime(cfg)

	const n = 64
	versions := make([]uint64, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			versions[i] = rt.Reload(cfg)
		}(i)
	}
	wg.Wait()

	seen := map[uint64]bool{1: true} // the initial snapshot
	for _, v := range versions {
		if seen[v] {
			t.Fatalf("duplicate version %d published", v)
		}
		seen[v] = true
	}
	if len(seen) != n+1 {
		t.Fatalf("got %d distinct versions, want %d", len(seen), n+1)
	}
}

// TestMaxConnsRejectsExtraConnection checks that a listener with max_conns=1
// refuses a second concurrent connection.
func TestMaxConnsRejectsExtraConnection(t *testing.T) {
	addr, _ := newTestServer(t, func(lc *config.ListenerConfig) { lc.MaxConns = 1 })

	first := roundTrip(t, addr)
	defer first.Close()

	second, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial second: %v", err)
	}
	defer second.Close()
	_ = second.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if n, err := second.Read(buf); err == nil {
		t.Fatalf("second connection read %d bytes, want it closed by the limit", n)
	}
}

// TestCancelClosesConnection checks that cancelling the server context tears the
// proxied connection down (ctx reaches the copy loop).
func TestCancelClosesConnection(t *testing.T) {
	echoAddr := startEcho(t)
	cfg := &config.Config{
		Version:   1,
		Listeners: []config.ListenerConfig{{Name: "edge", Addr: "127.0.0.1:0", UpstreamPool: "web", DialTimeout: config.Duration(time.Second)}},
		Pools:     map[string]config.PoolConfig{"web": {Servers: []string{echoAddr}}},
	}
	srv := NewServer(testLogger(), NewRuntime(cfg))

	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		shutCtx, c := context.WithTimeout(context.Background(), 2*time.Second)
		defer c()
		_ = srv.Shutdown(shutCtx)
	})

	conn := roundTrip(t, srv.Addrs()[0].String())
	defer conn.Close()

	cancel()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("connection still open after context cancellation")
	}
}
