// Package config defines the on-disk configuration schema and loading.
//
// It is intentionally pure data + validation, with no runtime state. The
// runtime view (compiled objects, live counters) lives in the proxy package.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults and schema constants.
const (
	// DefaultDialTimeout is the upstream dial timeout used when a listener
	// omits dial_timeout.
	DefaultDialTimeout = 5 * time.Second

	// DefaultIdleTimeout is the per-direction idle timeout used when a listener
	// omits idle_timeout.
	//
	// NOTE: an idle timeout is enforced on a buffered copy path, which disables
	// the zero-copy splice(2) fast path. Keep the default at 0 (disabled) to
	// preserve zero-copy by default; set idle_timeout explicitly in production
	// to reap half-open connections.
	DefaultIdleTimeout = 0 * time.Second

	// ConfigVersion is the only schema version this build understands.
	ConfigVersion = 1
)

// Config is the root of the configuration file.
type Config struct {
	Version   int                   `yaml:"version"`   // schema version (must be 1)
	Listeners []ListenerConfig      `yaml:"listeners"` // front-facing TCP listeners
	Pools     map[string]PoolConfig `yaml:"pools"`     // backend groups, keyed by pool name
}

// ListenerConfig describes a single front-facing TCP listener.
type ListenerConfig struct {
	Name         string   `yaml:"name"`          // unique name (used for logs and lookup)
	Addr         string   `yaml:"addr"`          // listen address, e.g. "127.0.0.1:8080"
	UpstreamPool string   `yaml:"upstream_pool"` // name of the target pool in Config.Pools
	MaxConns     int      `yaml:"max_conns"`     // max concurrent connections; 0 = unlimited
	DialTimeout  Duration `yaml:"dial_timeout"`  // upstream dial timeout (default 5s)
	IdleTimeout  Duration `yaml:"idle_timeout"`  // per-direction idle timeout; 0 disables (default 0)
}

// PoolConfig describes a group of backend endpoints.
type PoolConfig struct {
	Balance string `yaml:"balance"` // "round_robin" (default) or "least_conn"
	// Servers are backend "host:port" endpoints. Order is significant:
	// round-robin visits them in order and least-conn breaks ties by it, so do
	// not sort or reorder them casually.
	Servers []string `yaml:"servers"`
}

// Load reads, parses and validates the configuration file at path.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true) // reject unknown keys instead of silently ignoring typos

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.applyDefaultsAndValidate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyDefaultsAndValidate fills in default timeouts and rejects invalid
// configurations: wrong schema version, missing or duplicate listener names,
// malformed addresses, unknown pool references, negative limits/timeouts, empty
// pools, duplicate servers and unknown balance strategies. It mutates c in
// place and is called by Load.
func (c *Config) applyDefaultsAndValidate() error {
	if c.Version != ConfigVersion {
		return fmt.Errorf("config: unsupported version %d (want %d)", c.Version, ConfigVersion)
	}
	if len(c.Listeners) == 0 {
		return fmt.Errorf("config: no listeners defined")
	}

	seen := make(map[string]bool, len(c.Listeners))
	for i := range c.Listeners {
		l := &c.Listeners[i]
		switch {
		case l.Name == "":
			return fmt.Errorf("listener[%d]: name is required", i)
		case seen[l.Name]:
			return fmt.Errorf("listener %q: duplicate name", l.Name)
		case l.Addr == "":
			return fmt.Errorf("listener %q: addr is required", l.Name)
		case l.UpstreamPool == "":
			return fmt.Errorf("listener %q: upstream_pool is required", l.Name)
		}
		seen[l.Name] = true

		if err := validateAddr(l.Addr, false); err != nil {
			return fmt.Errorf("listener %q: %w", l.Name, err)
		}
		if _, ok := c.Pools[l.UpstreamPool]; !ok {
			return fmt.Errorf("listener %q: unknown upstream_pool %q", l.Name, l.UpstreamPool)
		}
		if l.DialTimeout == 0 {
			l.DialTimeout = Duration(DefaultDialTimeout)
		} else if l.DialTimeout < 0 {
			return fmt.Errorf("listener %q: dial_timeout cannot be negative", l.Name)
		}
		if l.IdleTimeout == 0 {
			l.IdleTimeout = Duration(DefaultIdleTimeout)
		} else if l.IdleTimeout < 0 {
			return fmt.Errorf("listener %q: idle_timeout cannot be negative", l.Name)
		}
		if l.MaxConns < 0 {
			return fmt.Errorf("listener %q: max_conns cannot be negative", l.Name)
		}
	}

	for name, p := range c.Pools {
		if len(p.Servers) == 0 {
			return fmt.Errorf("pool %q: at least one server is required", name)
		}
		switch p.Balance {
		case "", "round_robin", "least_conn":
		default:
			return fmt.Errorf("pool %q: unknown balance %q (want round_robin or least_conn)", name, p.Balance)
		}
		seenServer := make(map[string]bool, len(p.Servers))
		for _, s := range p.Servers {
			if err := validateAddr(s, true); err != nil {
				return fmt.Errorf("pool %q: %w", name, err)
			}
			if seenServer[s] {
				return fmt.Errorf("pool %q: duplicate server %q", name, s)
			}
			seenServer[s] = true
		}
	}
	return nil
}

// validateAddr checks that addr is a well-formed "host:port".
//
// When requireHost is true the host part must be present (used for upstream
// servers); listeners may omit it to bind all interfaces (":8080"). Port 0 is
// allowed for listeners (ephemeral port, handy in tests) but not for servers.
// The port must be numeric; service names are rejected to keep behaviour
// predictable.
func validateAddr(addr string, requireHost bool) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid address %q: %w", addr, err)
	}
	if requireHost && host == "" {
		return fmt.Errorf("invalid address %q: host is required", addr)
	}
	if port == "" {
		return fmt.Errorf("invalid address %q: port is required", addr)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("invalid address %q: port %q is not numeric", addr, port)
	}
	if n < 0 || n > 65535 {
		return fmt.Errorf("invalid address %q: port %d out of range 0..65535", addr, n)
	}
	if requireHost && n == 0 {
		return fmt.Errorf("invalid address %q: port 0 is not allowed for servers", addr)
	}
	return nil
}
