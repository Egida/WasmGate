package observe

import (
	"context"
	"log/slog"
	"testing"
)

// Tests for log.go: the log-level parsing used on the startup path. Text names
// exist only at the CLI/config boundary; inside the program everyone passes
// slog.Level.

// TestParseLevel pins the text-to-level mapping exactly, including case and
// surrounding whitespace tolerance.
func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":     slog.LevelDebug,
		"DEBUG":     slog.LevelDebug,
		"  debug  ": slog.LevelDebug,
		"info":      slog.LevelInfo,
		"warn":      slog.LevelWarn,
		"warning":   slog.LevelWarn,
		"WARN":      slog.LevelWarn,
		"error":     slog.LevelError,
	}
	for in, want := range cases {
		got, err := ParseLevel(in)
		if err != nil {
			t.Errorf("ParseLevel(%q): unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestParseLevelRejectsUnknown: a typo must be an error, not a silent fallback
// to Info — otherwise a bad -log-level quietly hides the logs.
func TestParseLevelRejectsUnknown(t *testing.T) {
	for _, in := range []string{"", "bogus", "fatal", "10", "debugg", "none"} {
		if _, err := ParseLevel(in); err == nil {
			t.Errorf("ParseLevel(%q): expected error, got none", in)
		}
	}
}

// TestNewLoggerLevelMapping pins the level filter exactly: the logger must
// enable its own level and not the level below it (Enabled alone cannot tell
// Info from Debug, since it reports "at least this level").
func TestNewLoggerLevelMapping(t *testing.T) {
	for _, lvl := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		l := NewLogger(lvl)
		if !l.Enabled(context.Background(), lvl) {
			t.Errorf("NewLogger(%s): %s is not enabled", lvl, lvl)
		}
		if lvl > slog.LevelDebug && l.Enabled(context.Background(), lvl-1) {
			t.Errorf("NewLogger(%s): level is below %s", lvl, lvl)
		}
	}
}
