package lib

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/schollz/progressbar/v3"
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

// copyFile creats a copy of src file at dstFinal.
// It creates the copy first a temporary file and then renames it to dstFinal.
// It shares its progress via bar.
func copyFile(src, dstFinal string, size int64, modTime time.Time, bar *progressbar.ProgressBar) error {
	dstTmp := dstFinal + ".tmp"

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	// Ensure target directories exists.
	baseName := filepath.Dir(dstFinal)
	if err := os.MkdirAll(baseName, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create dir %s: %w", baseName, err)
	}

	dstTmpFile, err := os.Create(dstTmp)
	if err != nil {
		return err
	}
	defer func() {
		if dstTmpFile != nil {
			dstTmpFile.Close()
		}
	}()

	buf := make([]byte, 1024*1024)

	for {
		n, err := srcFile.Read(buf)
		if err != nil && err != io.EOF {
			return fmt.Errorf("failed to read file %s: %w", src, err)
		}
		if n == 0 {
			break
		}

		if _, err := dstTmpFile.Write(buf[:n]); err != nil {
			return fmt.Errorf("failed to write file %s: %w", dstTmp, err)
		}

		if bar != nil {
			bar.Add(n)
		}
	}

	if err := dstTmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close dst tmp file %s: %w", dstTmp, err)
	}
	dstTmpFile = nil

	// TODO: do chtimes before rename?
	if err := os.Rename(dstTmp, dstFinal); err != nil {
		return fmt.Errorf("failed to rename %s: %w", dstTmp, err)
	}

	if err := os.Chtimes(dstFinal, modTime, modTime); err != nil {
		return err
	}

	return nil
}
