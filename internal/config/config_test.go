package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"wasmgate/internal/config"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wasmgate.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := write(t, `
version: 1
listeners:
  - name: edge
    addr: "127.0.0.1:0"
    upstream_pool: web
pools:
  web:
    balance: round_robin
    servers: ["127.0.0.1:9000"]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Listeners[0].DialTimeout.Std(); got != config.DefaultDialTimeout {
		t.Fatalf("dial timeout = %s, want %s", got, config.DefaultDialTimeout)
	}
	if got := cfg.Listeners[0].IdleTimeout.Std(); got != config.DefaultIdleTimeout {
		t.Fatalf("idle timeout = %s, want %s", got, config.DefaultIdleTimeout)
	}
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
	if got := cfg.Listeners[0].DialTimeout.Std(); got != 1500*time.Millisecond {
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
