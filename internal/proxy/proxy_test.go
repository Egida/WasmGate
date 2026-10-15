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
