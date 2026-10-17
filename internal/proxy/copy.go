package proxy

import (
	"context"
	"io"
	"net"
	"runtime"
	"sync"
	"time"
)

// idleBufferSize is the size of the pooled copy buffers. 32 KiB matches
// io.Copy's own default and keeps syscalls amortised.
const idleBufferSize = 32 * 1024

// bufferPool recycles copy buffers, so a relayed connection allocates no
// per-connection buffer. It backs the idle-aware path on every platform, and
// the plain path on the platforms without kernel zero-copy (see copyPlain).
var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, idleBufferSize)
		return &b
	},
}

// join pumps bytes in both directions until both are done, or until ctx is
// cancelled.
//
// Two copy paths exist:
//
//   - idle == 0: the plain relay, copyPlain. On Linux it delegates to
//     io.Copy(dst, src), which reaches TCPConn.ReadFrom and thereby splice(2):
//     the kernel moves bytes between the two sockets with no user-space copy and
//     no buffer allocation. That is our zero-copy fast path. Off Linux there is
//     no such syscall, so copyPlain relays through the same standard
//     io.CopyBuffer loop with a pooled buffer instead.
//   - idle > 0: a connection-wide idle watchdog. The deadline expires only when
//     the whole connection has been silent for the whole idle period: a long
//     one-way transfer (a slow download, an SSE stream) keeps the silent
//     back-channel alive. This path must inspect every chunk, so it buffers
//     (through the pool) and never splices.
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
	if idle <= 0 {
		return copyPlain(dst, src)
	}
	return copyWatched(dst, src, idle)
}

// copyPlain relays src -> dst with no idle watchdog.
//
// On Linux io.Copy reaches TCPConn.ReadFrom, which is implemented with
// splice(2) when both ends are sockets: the kernel moves the bytes, and
// nothing is allocated. Elsewhere there is no such syscall, and io.Copy falls
// back to genericReadFrom / io.copyBuffer — a fresh 32 KiB buffer per
// direction, ~64 KiB of garbage per connection. Handing the standard
// io.CopyBuffer loop one buffer from bufferPool instead (with the
// destination's fast path hidden behind a bare io.Writer) removes that
// garbage; the same trick cannot be applied unconditionally, because on Linux
// hiding TCPConn.ReadFrom would hide the path to splice itself.
//
// What remains is two interface wrappers per connection, ~64 B: the whole
// price of the pool.
func copyPlain(dst, src net.Conn) error {
	if runtime.GOOS == "linux" {
		_, ioErr := io.Copy(dst, src)
		return ioErr
	}
	bufp := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(bufp)
	_, err := io.CopyBuffer(struct{ io.Writer }{dst}, struct{ io.Reader }{src}, *bufp)
	return err
}

// copyWatched copies src -> dst through a pooled buffer while refreshing the
// idle deadline on both endpoints. It is copyPlain's pooled relay plus the
// watchdog: with idle > 0 every chunk must be inspected anyway, so splice
// stays out of reach by construction (the source is an activityReader and the
// destination is handed over as a bare io.Writer).
func copyWatched(dst, src net.Conn, idle time.Duration) error {
	bufp := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(bufp)
	r := &activityReader{conn: src, peer: dst, idle: idle}
	_, err := io.CopyBuffer(struct{ io.Writer }{dst}, r, *bufp)
	return err
}

// activityReader refreshes a read deadline on BOTH endpoints before its source
// is read from, turning the deadline into a connection-wide idle timeout: any
// byte in either direction keeps the whole connection alive. Refreshing only
// the source (a per-direction watchdog) would reap one-way transfers — a slow
// download or an SSE stream — mid-stream, while the back-channel is expected to
// be silent.
//
// Note: because it is not a *net.TCPConn, io.Copy cannot use splice(2) for this
// direction — that is the price of an idle watchdog. Set idle_timeout to 0 to
// keep the zero-copy path.
type activityReader struct {
	conn net.Conn
	peer net.Conn
	idle time.Duration
}

func (r *activityReader) Read(p []byte) (int, error) {
	deadline := time.Now().Add(r.idle)
	if err := r.conn.SetReadDeadline(deadline); err != nil {
		return 0, err
	}
	_ = r.peer.SetReadDeadline(deadline)
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
