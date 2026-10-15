package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that can be decoded from YAML strings such as
// "3s" or "1m30s".
//
// Why this type exists: time.Duration does NOT implement yaml.Unmarshaler and
// yaml.v3 has no built-in hook for it, so `dial_timeout: 3s` would fail to
// decode into a plain time.Duration field. Wrapping it and implementing
// UnmarshalYAML fixes that and keeps the config human-friendly.
type Duration time.Duration

// Std returns the standard library value.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string like \"3s\": %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}
