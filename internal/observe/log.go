// Package observe owns logging, metrics and (later) tracing.
//
// For the first iteration we use the standard library log/slog: zero
// dependencies and structured output. It can be swapped for zerolog later
// without touching call sites, because everything goes through NewLogger.
package observe

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger builds a structured logger writing to stderr.
func NewLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler)
}
