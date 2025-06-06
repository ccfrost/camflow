package commands

import (
	"log/slog"
	"os"
)

var logger *slog.Logger

func init() {
	// Check for debug/verbose environment variables
	debugMode := os.Getenv("DEBUG") != "" || os.Getenv("VERBOSE") != ""

	var level slog.Level
	if debugMode {
		level = slog.LevelDebug
	} else {
		level = slog.LevelInfo
	}

	// Create handler options
	opts := &slog.HandlerOptions{
		Level: level,
	}

	// Use TextHandler for human-readable output
	logger = slog.New(slog.NewTextHandler(os.Stderr, opts))
}
