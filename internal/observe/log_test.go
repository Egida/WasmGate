package observe

import (
	"context"
	"log/slog"
	"testing"
)

// Tests for log.go: the log-level string mapping used on the startup path.

// TestNewLoggerLevelMapping pins the mapping exactly: the logger must enable its
// own level and not the level below it (Enabled alone cannot tell Info from
// Debug, since it reports "at least this level").
func TestNewLoggerLevelMapping(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":     slog.LevelDebug,
		"DEBUG":     slog.LevelDebug,
		"  debug  ": slog.LevelDebug,
		"warn":      slog.LevelWarn,
		"warning":   slog.LevelWarn,
		"error":     slog.LevelError,
		"info":      slog.LevelInfo,
		"":          slog.LevelInfo,
		"bogus":     slog.LevelInfo,
	}
	for in, want := range cases {
		l := NewLogger(in)
		if !l.Enabled(context.Background(), want) {
			t.Errorf("NewLogger(%q): %s is not enabled", in, want)
		}
		if want > slog.LevelDebug && l.Enabled(context.Background(), want-1) {
			t.Errorf("NewLogger(%q): level is below %s", in, want)
		}
	}
}
