// Package observe owns logging, metrics and the admin endpoint — everything
// that makes the running proxy observable.
//
// For the first iteration we use the standard library log/slog: zero
// dependencies and structured output. It can be swapped for zerolog later
// without touching call sites, because everything goes through NewLogger.
package observe

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// ParseLevel converts a textual log level ("debug", "info", "warn", "warning",
// "error") into a slog.Level.
//
// Text exists only at the boundaries (the CLI flag, later the config file);
// everywhere else the program passes slog.Level around — the type-safe enum
// the standard library already provides, so there is nothing of ours to keep
// in sync. An unknown string is an error, not a silent fallback: a typo in
// -log-level should stop the process, not quietly hide the logs.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("observe: unknown log level %q (want debug, info, warn or error)", s)
	}
}

// NewLogger builds a structured logger writing to stderr at the given level.
func NewLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}
