// Package config defines the on-disk configuration schema and loading.
//
// It is intentionally pure data + validation, with no runtime state. The
// runtime view (compiled objects, live counters) lives in the proxy package.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults applied when the value is omitted in the file.
const (
	DefaultDialTimeout = 5 * time.Second
	DefaultIdleTimeout = 60 * time.Second
)

// Config is the root of the configuration file.
type Config struct {
	Version   int                   `yaml:"version"`   // schema version (informational)
	Listeners []ListenerConfig      `yaml:"listeners"` // front-facing TCP listeners
	Pools     map[string]PoolConfig `yaml:"pools"`     // backend groups, keyed by pool name
}

// ListenerConfig describes a single front-facing TCP listener.
type ListenerConfig struct {
	Name         string   `yaml:"name"`          // unique name (used for logs and lookup)
	Addr         string   `yaml:"addr"`          // listen address, e.g. "127.0.0.1:8080"
	UpstreamPool string   `yaml:"upstream_pool"` // name of the target pool in Config.Pools
	MaxConns     int      `yaml:"max_conns"`     // RESERVED (M2): not enforced yet
	DialTimeout  Duration `yaml:"dial_timeout"`  // upstream dial timeout (default 5s)
	IdleTimeout  Duration `yaml:"idle_timeout"`  // RESERVED (M2): not enforced yet (default 60s)
}

// PoolConfig describes a group of backend endpoints.
type PoolConfig struct {
	Balance string   `yaml:"balance"` // "round_robin" (default) or "least_conn"
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

// Listener returns the listener with the given name.
func (c *Config) Listener(name string) (ListenerConfig, bool) {
	for i := range c.Listeners {
		if c.Listeners[i].Name == name {
			return c.Listeners[i], true
		}
	}
	return ListenerConfig{}, false
}

// applyDefaultsAndValidate fills in default timeouts and rejects invalid
// configurations: missing or duplicate listener names, unknown pool references,
// negative connection limits, empty pools and unknown balance strategies. It
// mutates c in place and is called by Load.
func (c *Config) applyDefaultsAndValidate() error {
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

		if _, ok := c.Pools[l.UpstreamPool]; !ok {
			return fmt.Errorf("listener %q: unknown upstream_pool %q", l.Name, l.UpstreamPool)
		}
		if l.DialTimeout == 0 {
			l.DialTimeout = Duration(DefaultDialTimeout)
		}
		if l.IdleTimeout == 0 {
			l.IdleTimeout = Duration(DefaultIdleTimeout)
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
	}
	return nil
}
