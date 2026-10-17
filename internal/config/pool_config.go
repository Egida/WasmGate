package config

import (
	"errors"
	"fmt"
)

// PoolConfig describes a group of backend endpoints.
type PoolConfig struct {
	Balance string `yaml:"balance"` // "round_robin" (default) or "least_conn"
	// Servers are backend "host:port" endpoints. Order is significant:
	// round-robin visits them in order and least-conn breaks ties by it, so do
	// not sort or reorder them casually.
	Servers []string `yaml:"servers"`
}

// Validate rejects invalid pools: an unknown balance strategy, and malformed or
// duplicated servers. Balance "" is accepted and means round_robin; server
// order is meaningful and is left untouched.
func (p PoolConfig) Validate() error {
	if len(p.Servers) == 0 {
		return errors.New("at least one server is required")
	}
	switch p.Balance {
	case "", "round_robin", "least_conn":
	default:
		return fmt.Errorf("unknown balance %q (want round_robin or least_conn)", p.Balance)
	}
	seen := make(map[string]bool, len(p.Servers))
	for _, s := range p.Servers {
		if err := validateAddr(s, true); err != nil {
			return err
		}
		if seen[s] {
			return fmt.Errorf("duplicate server %q", s)
		}
		seen[s] = true
	}
	return nil
}
