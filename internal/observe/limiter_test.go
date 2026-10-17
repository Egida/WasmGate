package observe

import (
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// Tests for limiter.go: the per-key log rate limiter, followed by the benchmarks
// for the paths that matter under load. Tests drive the injectable clock (now)
// instead of sleeping, so they are deterministic.

// fixedClock returns a Now function reading from *at, which the test advances.
func fixedClock(at *time.Time) func() time.Time {
	return func() time.Time { return *at }
}

// sinkAllowed and sinkDropped stop the compiler from optimising the benchmarked
// call away.
var (
	sinkAllowed bool
	sinkDropped int
)

func TestLimiterDisabledAllowsEverything(t *testing.T) {
	for _, limit := range []int{0, -1} {
		l := NewLimiter(time.Second, limit)
		for i := 0; i < 100; i++ {
			if ok, dropped := l.Allow("k"); !ok || dropped != 0 {
				t.Fatalf("limit %d, call %d: allowed=%v dropped=%d, want true/0", limit, i, ok, dropped)
			}
		}
		if len(l.keys) != 0 {
			t.Fatalf("limit %d: disabled limiter allocated %d keys", limit, len(l.keys))
		}
	}
}

func TestLimiterAllowsUpToLimitPerWindow(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(time.Second, 2)
	l.now = fixedClock(&now)

	for i := 0; i < 2; i++ {
		if ok, dropped := l.Allow("k"); !ok || dropped != 0 {
			t.Fatalf("call %d: allowed=%v dropped=%d, want true/0", i, ok, dropped)
		}
	}
	for i := 0; i < 3; i++ {
		if ok, dropped := l.Allow("k"); ok || dropped != 0 {
			t.Fatalf("call above the limit: allowed=%v dropped=%d, want false/0", ok, dropped)
		}
	}
}

func TestLimiterReportsSuppressedOnNextAllowed(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(time.Second, 1)
	l.now = fixedClock(&now)

	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("first event was dropped")
	}
	for i := 0; i < 3; i++ {
		l.Allow("k") // suppressed
	}

	now = now.Add(time.Second) // the window rolls over
	ok, dropped := l.Allow("k")
	if !ok {
		t.Fatal("event after the window rolled over was dropped")
	}
	if dropped != 3 {
		t.Fatalf("dropped = %d, want 3 (the previous window's suppressed events)", dropped)
	}
}

func TestLimiterWindowBoundary(t *testing.T) {
	start := time.Unix(0, 0)
	now := start
	l := NewLimiter(time.Second, 1)
	l.now = fixedClock(&now)

	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("first event was dropped")
	}

	now = start.Add(time.Second - time.Nanosecond) // still the same window
	if ok, _ := l.Allow("k"); ok {
		t.Fatal("event admitted just before the window ended")
	}

	now = start.Add(time.Second) // exactly one window later
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("event refused at the window boundary")
	}
}

func TestLimiterKeysAreIndependent(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(time.Second, 1)
	l.now = fixedClock(&now)

	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("first event for a was dropped")
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("second event for a was admitted")
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Fatal("first event for b was dropped: keys are not independent")
	}
}

// BenchmarkLimiterAllow measures the hot path for a single repeated key. limit 0
// is the no-op case (the limiter is configured off), limit 2 exercises the
// throttle-and-drop path.
func BenchmarkLimiterAllow(b *testing.B) {
	cases := []struct {
		name  string
		limit int
	}{
		{"noop_limit0", 0},
		{"throttled_limit2", 2},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			l := NewLimiter(time.Second, c.limit)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sinkAllowed, sinkDropped = l.Allow("connection limit reached")
			}
		})
	}
}

// BenchmarkLimiterAllowManyKeys measures the path that touches the key map with
// many distinct keys (one per listener/upstream), the worst case for the mutex
// and the map.
func BenchmarkLimiterAllowManyKeys(b *testing.B) {
	const keySpace = 1024 // power of two: replaces modulo with a mask
	keys := make([]string, keySpace)
	for i := range keys {
		keys[i] = "dial:edge:127.0.0.1:" + strconv.Itoa(9000+i)
	}

	l := NewLimiter(time.Second, 2)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sinkAllowed, sinkDropped = l.Allow(keys[i&(keySpace-1)])
	}
}

// BenchmarkLimiterAllowParallel measures Allow with every P competing for the
// limiter. The single-goroutine cases above cannot show this: with one goroutine
// the mutex is always free, so they measure the map and the counter, not the
// lock. This is the case that matters, because the limiter is called precisely
// when many goroutines fail at once (a connection flood, a dead upstream).
//
// shared_key points every goroutine at one key, i.e. one listener's event storm.
// distinct_keys gives each goroutine its own key: if the cost barely moves, the
// bottleneck is the one table lock and not the key space, and the number is the
// price of the limiter under load rather than a property of the key.
func BenchmarkLimiterAllowParallel(b *testing.B) {
	b.Run("shared_key", func(b *testing.B) {
		l := NewLimiter(time.Second, 2)
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				sinkAllowed, sinkDropped = l.Allow("connection limit reached")
			}
		})
	})

	b.Run("distinct_keys", func(b *testing.B) {
		l := NewLimiter(time.Second, 2)
		var n atomic.Uint64
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			// One key per goroutine: no two goroutines share a bucket, so what
			// is left is the table lock itself.
			key := "dial:edge:127.0.0.1:" + strconv.FormatUint(n.Add(1), 10)
			for pb.Next() {
				sinkAllowed, sinkDropped = l.Allow(key)
			}
		})
	})
}
