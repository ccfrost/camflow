package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// videoStagingDir returns the path to the video staging directory.
// Note that the path may not exist.
func videoStagingDir() (string, error) {
	const appName = "camedia"

	var baseCacheDir string
	var err error

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not get user home directory: %w", err)
	}

	switch runtime.GOOS {
	case "linux":
		xdgCacheHome := os.Getenv("XDG_CACHE_HOME")
		if xdgCacheHome != "" && filepath.IsAbs(xdgCacheHome) {
			baseCacheDir = xdgCacheHome
		} else {
			baseCacheDir = filepath.Join(homeDir, ".cache")
		}

	case "darwin":
		baseCacheDir = filepath.Join(homeDir, "Library", "Application Support")

	default:
		return "", fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}

	appStagingDir := filepath.Join(baseCacheDir, appName, "video-staging")

	return appStagingDir, nil
}
