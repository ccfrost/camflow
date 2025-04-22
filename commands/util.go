package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// TODO: delete
/*
// videoStagingDirFunc defines the type for the function that gets the staging directory.
// This allows it to be replaced during testing.
var videoStagingDirFunc = defaultVideoStagingDir

// defaultVideoStagingDir returns the default path to the video staging directory.
// It uses XDG base directory specification.
func defaultVideoStagingDir() (string, error) {
	// TODO: CacheHome can be "".
	stagingDir := filepath.Join(xdg.CacheHome, "camedia", "staging", "videos")
	err := os.MkdirAll(stagingDir, 0750) // Ensure the directory exists
	if err != nil {
		return "", fmt.Errorf("failed to create video staging directory %s: %w", stagingDir, err)
	}
	return stagingDir, nil
}

// videoStagingDir returns the path to the video staging directory.
// It uses the function stored in videoStagingDirFunc.
func videoStagingDir() (string, error) {
	return videoStagingDirFunc()
}
*/

// videoStagingDir returns the path to the video staging directory.
// Note that the path may not exist.
func videoStagingDir() (string, error) {
	const appName = "camedia"

	var baseCacheDir string
	var err error

	// TODO: consider using xdg.CacheHome for some part of the below.

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
