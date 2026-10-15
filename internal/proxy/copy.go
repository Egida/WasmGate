package proxy

import (
	"io"
	"net"
	"sync"
)

// join pumps bytes in both directions until both are done.
//
// io.Copy is deliberately used instead of a hand-written loop: when the
// destination is a *net.TCPConn, io.Copy calls TCPConn.ReadFrom, which on Linux
// is implemented with splice(2) — the kernel moves bytes between the two
// sockets without an extra user-space copy. This is our zero-copy fast path.
//
// Half-close is preserved: when one direction hits EOF we CloseWrite on the
// destination so protocols that rely on signalling EOF keep working, while the
// other direction can still flow.
//
// TODO(milestone M2): honor idle_timeout by refreshing read deadlines, and tear
// down both sides on a hard error so a half-open connection cannot linger.
func join(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go pipe(&wg, a, b) // a <- b
	go pipe(&wg, b, a) // b <- a
	wg.Wait()
}

// pipe copies src -> dst until EOF and then half-closes dst's write side. It is
// the building block of join and signals completion through wg.
func pipe(wg *sync.WaitGroup, dst, src net.Conn) {
	defer wg.Done()
	_, _ = io.Copy(dst, src)
	halfCloseWrite(dst)
}

// halfCloseWrite closes the write half if supported, otherwise the whole conn.
func halfCloseWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}
