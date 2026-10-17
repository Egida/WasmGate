package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wasmgate/internal/config"
)

// Tests for config.go: Load as a whole (parsing, version gate and the
// cross-entity validation). Per-object validation is covered next to the object:
// listener_config_test.go and pool_config_test.go. The benchmarks for the cold
// path (startup and every hot reload) are at the bottom of this file.

// write drops a config body into a temp file and returns its path. Shared by the
// tests and benchmarks of this package.
func write(tb testing.TB, body string) string {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "wasmgate.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		tb.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestLoadParsesDurationString(t *testing.T) {
	path := write(t, `
version: 1
listeners:
  - name: edge
    addr: "127.0.0.1:0"
    upstream_pool: web
    dial_timeout: 1500ms
pools:
  web:
    servers: ["127.0.0.1:9000"]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Listeners[0].DialTimeout; got != 1500*time.Millisecond {
		t.Fatalf("dial timeout = %s, want 1.5s", got)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	path := write(t, `
version: 1
bogus: true
listeners:
  - name: edge
    addr: "127.0.0.1:0"
    upstream_pool: web
pools:
  web:
    servers: ["127.0.0.1:9000"]
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

func TestLoadRejectsWrongVersion(t *testing.T) {
	path := write(t, `
version: 2
listeners:
  - name: edge
    addr: "127.0.0.1:0"
    upstream_pool: web
pools:
  web:
    servers: ["127.0.0.1:9000"]
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected error for unsupported version, got nil")
	}
}

// TestLoadRejectsUnknownPool covers the cross-entity check: a listener may only
// reference a pool that exists.
func TestLoadRejectsUnknownPool(t *testing.T) {
	path := write(t, `
version: 1
listeners:
  - name: edge
    addr: "127.0.0.1:0"
    upstream_pool: nope
pools:
  web:
    servers: ["127.0.0.1:9000"]
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected error for unknown pool, got nil")
	}
}

func TestLoadMissingFileFails(t *testing.T) {
	if _, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("Load of a missing file returned nil, want an error")
	}
}

func TestLoadRejectsNoListeners(t *testing.T) {
	path := write(t, `
version: 1
pools:
  web:
    servers: ["127.0.0.1:9000"]
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected error for a config without listeners, got nil")
	}
}

func TestLoadRejectsDuplicateListenerName(t *testing.T) {
	path := write(t, `
version: 1
listeners:
  - name: edge
    addr: "127.0.0.1:0"
    upstream_pool: web
  - name: edge
    addr: "127.0.0.1:0"
    upstream_pool: web
pools:
  web:
    servers: ["127.0.0.1:9000"]
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected error for a duplicate listener name, got nil")
	}
}

// TestLoadIdentifiesUnnamedListenerByIndex covers the error label: a listener
// without a name has to be reported by its index, otherwise the message would
// not say which entry is broken.
func TestLoadIdentifiesUnnamedListenerByIndex(t *testing.T) {
	path := write(t, `
version: 1
listeners:
  - addr: "127.0.0.1:0"
    upstream_pool: web
pools:
  web:
    servers: ["127.0.0.1:9000"]
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for a nameless listener, got nil")
	}
	if !strings.Contains(err.Error(), "listener[0]") {
		t.Fatalf("error %q does not identify the nameless listener by index", err)
	}
}

// sample is a representative multi-listener / multi-pool configuration.
const sample = `version: 1
listeners:
  - name: edge
    addr: "127.0.0.1:8080"
    upstream_pool: web
    max_conns: 10000
    dial_timeout: 5s
    idle_timeout: 0s
  - name: admin
    addr: "127.0.0.1:8081"
    upstream_pool: admin
pools:
  web:
    balance: least_conn
    servers: ["127.0.0.1:9001", "127.0.0.1:9002", "127.0.0.1:9003"]
  admin:
    servers: ["127.0.0.1:9100"]
`

// The benchmarks below cover the cold path taken at startup and on every hot
// reload (SIGHUP / control plane). Reload latency matters because a slow reload
// shortens the window in which a bad config can be detected.

// BenchmarkLoad measures the full reload path: open -> parse (KnownFields) ->
// validate. This is the per-SIGHUP cost.
func BenchmarkLoad(b *testing.B) {
	path := write(b, sample)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := config.Load(path); err != nil {
			b.Fatalf("load: %v", err)
		}
	}
}

// BenchmarkValidate measures validating an already-parsed config, i.e. a control
// plane calling Config.Validate before Runtime.Reload.
func BenchmarkValidate(b *testing.B) {
	cfg, err := config.Load(write(b, sample))
	if err != nil {
		b.Fatalf("load: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := cfg.Validate(); err != nil {
			b.Fatalf("validate: %v", err)
		}
	}
}

// badConfigErrorInLastListener builds a document of n valid listeners followed
// by one whose address is malformed. Validation stops at the first error, so
// this is the worst case for the reload path: the whole file is parsed and every
// earlier listener is validated before the bad one is reached.
func badConfigErrorInLastListener(n int) string {
	var sb strings.Builder
	sb.WriteString("version: 1\nlisteners:\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "  - name: l%d\n    addr: \"127.0.0.1:%d\"\n    upstream_pool: web\n", i, 9000+i)
	}
	sb.WriteString("  - name: boom\n    addr: \"127.0.0.1:not-a-port\"\n    upstream_pool: web\n")
	sb.WriteString("pools:\n  web:\n    servers: [\"127.0.0.1:9001\"]\n")
	return sb.String()
}

// BenchmarkLoadInvalid measures the rejection path, which is half the reason a
// reload has to be fast: a bad config must be thrown back at the operator
// quickly, and the refusal itself must not be more expensive than the happy
// path (otherwise a stream of broken configs becomes a way to stall reloads).
//
// The cases fail at different stages: YAML decode, cross-listener validation and
// per-listener validation. Every case is checked to actually fail, so a body
// that drifts into being valid cannot silently turn this into a second
// BenchmarkLoad.
func BenchmarkLoadInvalid(b *testing.B) {
	cases := []struct {
		name string
		body string
	}{
		{"unknown_field", strings.Replace(sample, "max_conns: 10000", "max_conn: 10000", 1)},
		{"duplicate_addr", strings.Replace(sample, `addr: "127.0.0.1:8081"`, `addr: "127.0.0.1:8080"`, 1)},
		{"bad_listener_addr", strings.Replace(sample, "127.0.0.1:8080", "127.0.0.1:not-a-port", 1)},
		{"error_in_last_listener", badConfigErrorInLastListener(500)},
	}
	for _, c := range cases {
		path := write(b, c.body)
		if _, err := config.Load(path); err == nil {
			b.Fatalf("case %q is valid, so it would benchmark the happy path", c.name)
		}
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := config.Load(path); err == nil {
					b.Fatal("invalid config was accepted")
				}
			}
		})
	}
}
