package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// New creates a JSON logger suitable for correlating process and request logs.
func New(output io.Writer, level string, service string) (*slog.Logger, error) {
	var parsed slog.Level
	switch strings.ToLower(level) {
	case "debug":
		parsed = slog.LevelDebug
	case "info":
		parsed = slog.LevelInfo
	case "warn":
		parsed = slog.LevelWarn
	case "error":
		parsed = slog.LevelError
	default:
		return nil, fmt.Errorf("SEA_LOG_LEVEL: unsupported level %q", level)
	}

	handler := slog.NewJSONHandler(output, &slog.HandlerOptions{Level: parsed})
	return slog.New(handler).With("service", service), nil
}
