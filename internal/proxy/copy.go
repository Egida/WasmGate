package proxy

import (
	"context"
	"io"
	"net"
	"sync"
	"time"
)

// join pumps bytes in both directions until both are done, or until ctx is
// cancelled.
//
// Two copy paths exist:
//
//   - idle == 0: plain io.Copy. When the destination is a *net.TCPConn, io.Copy
//     calls TCPConn.ReadFrom, which on Linux is implemented with splice(2) — the
//     kernel moves bytes between the two sockets without an extra user-space
//     copy. This is our zero-copy fast path.
//   - idle > 0: each direction refreshes a read deadline before every read, so a
//     connection that stays silent for longer than idle is reaped. This path
//     buffers bytes (splice is not used) but keeps half-open connections from
//     lingering forever.
//
// Half-close is preserved on a clean EOF: we CloseWrite the destination so
// protocols that rely on signalling EOF keep working while the other direction
// can still flow.
//
// On a hard error (reset, idle timeout, ...) BOTH sides are closed, so the peer
// direction can never stay blocked on a half-open connection.
func join(ctx context.Context, a, b net.Conn, idle time.Duration) {
	// When ctx is cancelled (e.g. server shutdown), close both sides so the copy
	// goroutines unblock and wg.Wait returns.
	stop := context.AfterFunc(ctx, func() {
		_ = a.Close()
		_ = b.Close()
	})
	defer stop()

	var wg sync.WaitGroup
	wg.Add(2)
	go pipe(&wg, a, b, idle) // a <- b
	go pipe(&wg, b, a, idle) // b <- a
	wg.Wait()
}

// pipe copies src -> dst until EOF or error, then reports completion via wg.
// A clean EOF half-closes dst's write side; any other outcome tears down both
// connections so the peer direction cannot stay blocked.
func pipe(wg *sync.WaitGroup, dst, src net.Conn, idle time.Duration) {
	defer wg.Done()

	if err := copyConn(dst, src, idle); err != nil {
		// Hard error or idle timeout: close both sides. Close is safe to call
		// from either pipe (the other one will simply observe a closed conn).
		_ = dst.Close()
		_ = src.Close()
		return
	}
	halfCloseWrite(dst)
}

// copyConn copies src -> dst, choosing the zero-copy path when idle is disabled
// and the idle-aware buffered path otherwise.
func copyConn(dst, src net.Conn, idle time.Duration) error {
	var r io.Reader = src
	if idle > 0 {
		r = &deadlineReader{conn: src, idle: idle}
	}
	_, err := io.Copy(dst, r)
	return err
}

// deadlineReader refreshes a read deadline before every read, turning it into an
// idle timeout.
//
// Note: because it is not a *net.TCPConn, io.Copy cannot use splice(2) for this
// direction — that is the price of an idle watchdog. Set idle_timeout to 0 to
// keep the zero-copy path.
type deadlineReader struct {
	conn net.Conn
	idle time.Duration
}

func (r *deadlineReader) Read(p []byte) (int, error) {
	if err := r.conn.SetReadDeadline(time.Now().Add(r.idle)); err != nil {
		return 0, err
	}
	return r.conn.Read(p)
}

// halfCloseWrite closes the write half if supported, otherwise the whole conn.
func halfCloseWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}
