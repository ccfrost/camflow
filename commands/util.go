package commands

import (
	"fmt"
	"os"
	"path/filepath"
)

// TODO: delete
/*
// videoTargetRootDirFunc defines the type for the function that gets the staging directory.
// This allows it to be replaced during testing.
var videoTargetRootDirFunc = defaultVideoTargetRootDir

// defaultVideoTargetRootDir returns the default path to the video staging directory.
// It uses XDG base directory specification.
func defaultVideoTargetRootDir() (string, error) {
	// TODO: CacheHome can be "".
	stagingDir := filepath.Join(xdg.CacheHome, "camflow", "staging", "videos")
	err := os.MkdirAll(stagingDir, 0750) // Ensure the directory exists
	if err != nil {
		return "", fmt.Errorf("failed to create video staging directory %s: %w", stagingDir, err)
	}
	return stagingDir, nil
}

// videoTargetRootDir returns the path to the video staging directory.
// It uses the function stored in videoTargetRootDirFunc.
func videoTargetRootDir() (string, error) {
	return videoTargetRootDirFunc()
}
*/

// videoTargetRootDir returns the path to the video staging directory.
// Note that the path may not exist.
// TODO: remove?
/*
func videoTargetRootDir() (string, error) {
	const appName = "camflow"

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

	appTargetRootDir := filepath.Join(baseCacheDir, appName, "video-staging")

	return appTargetRootDir, nil
}
*/

// getCacheDirPath determines where to store the a cache file with the given file base name.
func getCacheDirPath(cacheDirFlag, fileBaseName string) (string, error) {
	// Prefer user-specific cache dir if specified.
	if cacheDirFlag != "" {
		return filepath.Join(cacheDirFlag, fileBaseName), nil
	}

	// Fall back to user cache dir.
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "camflow", fileBaseName), nil
	}
	return "", fmt.Errorf("unable to determine cache dir")
}
