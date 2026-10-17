package observe

import (
	"sync"
	"time"
)

// Limiter rate-limits repeated log events by key.
//
// It exists because some events are per-connection and become a log storm under
// load — a rejected connection, a failed upstream dial, a future DDoS denial.
// Under a flood those are exactly the events you want summarised, not emitted
// a thousand times a second.
//
// It is intentionally tiny: one mutex guards a small per-key table, and the
// caller needs no state of its own. That does serialise concurrent callers
// (BenchmarkLimiterAllowParallel puts the cost of that contention on record) —
// acceptable because the alternative is emitting a log line per event, which
// costs orders of magnitude more than the lock.
type Limiter struct {
	every time.Duration // window length
	limit int           // max emitted events per key per window
	now   func() time.Time

	mu   sync.Mutex
	keys map[string]*bucket
}

type bucket struct {
	start time.Time // window start
	sent  int       // events already emitted in this window
	drop  int       // events suppressed since the last emitted one
}

// NewLimiter returns a limiter allowing up to limit events per key per every
// window. A non-positive limit allows everything (the limiter is a no-op).
func NewLimiter(every time.Duration, limit int) *Limiter {
	return &Limiter{every: every, limit: limit, now: time.Now, keys: make(map[string]*bucket)}
}

// Allow reports whether an event identified by key may be logged now. When it
// returns true, dropped is the number of events that were suppressed for key
// since the last allowed one (0 if none), so the caller can surface the loss
// without emitting a line per event.
func (l *Limiter) Allow(key string) (allowed bool, dropped int) {
	if l.limit <= 0 {
		return true, 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b := l.keys[key]
	if b == nil || now.Sub(b.start) >= l.every {
		// First event, or a fresh window: emit and report the previous window's
		// suppressed count (carried so the loss is visible on the next line).
		var carried int
		if b != nil {
			carried = b.drop
		}
		l.keys[key] = &bucket{start: now, sent: 1}
		return true, carried
	}
	if b.sent < l.limit {
		b.sent++
		carried := b.drop
		b.drop = 0
		return true, carried
	}
	b.drop++
	return false, 0
}
