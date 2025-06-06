package commands

import (
	"log/slog"
	"os"
)

var logger *slog.Logger

func init() {
	level := slog.LevelInfo
	if os.Getenv("DEBUG") != "" {
		level = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}
	logger = slog.New(slog.NewTextHandler(os.Stderr, opts))
}
