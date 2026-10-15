package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Server binds the configured listeners and dispatches accepted connections to
// the Proxy.
type Server struct {
	log   *slog.Logger
	rt    *Runtime
	proxy *Proxy

	mu sync.Mutex
	ln []net.Listener

	wg sync.WaitGroup
}

// NewServer builds a Server bound to the runtime rt.
func NewServer(log *slog.Logger, rt *Runtime) *Server {
	return &Server{log: log, rt: rt, proxy: New(log, rt)}
}

// Start binds every listener in the current snapshot and begins accepting.
func (s *Server) Start(ctx context.Context) error {
	snap := s.rt.Load()

	var lc net.ListenConfig
	for _, l := range snap.Config.Listeners {
		ln, err := lc.Listen(ctx, "tcp", l.Addr)
		if err != nil {
			s.closeListeners() // release anything already bound
			return fmt.Errorf("listen %q on %s: %w", l.Name, l.Addr, err)
		}

		s.mu.Lock()
		s.ln = append(s.ln, ln)
		s.mu.Unlock()

		s.log.Info("listening",
			"listener", l.Name,
			"addr", ln.Addr().String(),
			"pool", l.UpstreamPool,
		)

		s.wg.Add(1)
		go s.acceptLoop(ctx, l.Name, ln)
	}
	return nil
}

// Addrs returns the bound listener addresses (useful in tests with port 0).
func (s *Server) Addrs() []net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	addrs := make([]net.Addr, 0, len(s.ln))
	for _, ln := range s.ln {
		addrs = append(addrs, ln.Addr())
	}
	return addrs
}

// closeListeners closes every bound listener. Idempotent.
func (s *Server) closeListeners() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ln := range s.ln {
		_ = ln.Close()
	}
}

func (s *Server) acceptLoop(ctx context.Context, name string, ln net.Listener) {
	defer s.wg.Done()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return // listener closed during shutdown
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue // transient, retry immediately
			}
			// Back off briefly so a persistent error cannot spin the CPU.
			s.log.Warn("accept error", "listener", name, "err", err)
			select {
			case <-time.After(50 * time.Millisecond):
			case <-ctx.Done():
				return
			}
			continue
		}

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.proxy.Handle(ctx, name, conn)
		}()
	}
}

// Shutdown stops accepting and waits for in-flight connections to drain, or
// until ctx is done.
func (s *Server) Shutdown(ctx context.Context) error {
	s.closeListeners()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
