package commands

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/ccfrost/camflow/camflowconfig"
)

// UploadVideos uploads videos from the video export queue dir to Google Photos.
// Videos are added to all albums in config.DefaultAlbums.
// Uploaded videos are moved from export queue to VideosExportedRoot unless keepQueued is true.
// The function is idempotent - if interrupted, it can be recalled to resume.
// Takes configDir to locate token and cache files, and a gphotosClient for API interaction.
func UploadVideos(ctx context.Context, config camflowconfig.CamediaConfig, cacheDirFlag string, keepQueued bool, gphotosClient GPhotosClient) error {
	if err := config.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	return uploadMediaItems(ctx, cacheDirFlag, keepQueued, &config.LocalVideos, &config.GooglePhotos.Videos, "videos", gphotosClient)
}

// cleanupEmptyParentDirs removes empty parent directories of the moved video file
// within the export queue area. It cleans directories from the video's original parent
// up to, but not including, the exportQueueDir.
// It returns an error if any unexpected issue occurs during the cleanup process.
func cleanupEmptyParentDirs(videoPath string, rawExportQueueRoot string) error {
	// This logic cleans up empty parent directories of the moved video,
	// up to, but not including, the export queue root directory.
	exportQueueRoot := filepath.Clean(rawExportQueueRoot)
	currentDirToClean := filepath.Clean(filepath.Dir(videoPath))

	// Loop to remove empty parent directories.
	// The loop stops if currentDirToClean is the export queue root,
	// is outside the export queue root, or is not empty.
	for {
		// Stop if currentDirToClean is no longer a strict subdirectory of exportQueueRoot,
		// or if it's the export queue root itself, or a root-like path.
		// We use strings.HasPrefix to ensure we are within the export queue root's path.
		// We also check currentDirToClean != exportQueueRoot to ensure we don't process the root itself.
		if !strings.HasPrefix(currentDirToClean, exportQueueRoot) ||
			currentDirToClean == exportQueueRoot ||
			currentDirToClean == "." ||
			currentDirToClean == string(os.PathSeparator) {
			return nil
		}

		// Ensure currentDirToClean is actually a child of exportQueueRoot, not just sharing a prefix.
		// e.g. /tmp/export-queue-other should not be processed if root is /tmp/export-queue.
		if !strings.HasPrefix(currentDirToClean, exportQueueRoot+string(os.PathSeparator)) {
			return nil
		}

		entries, err := os.ReadDir(currentDirToClean)
		if err != nil {
			if os.IsNotExist(err) {
				// Directory already gone, try its parent.
				parent := filepath.Dir(currentDirToClean)
				if parent == currentDirToClean {
					// Safety break if at filesystem root
					return nil
				}
				currentDirToClean = parent
				continue
			}
			return fmt.Errorf("error reading directory %s during cleanup: %w", currentDirToClean, err)
		}

		if len(entries) != 0 {
			// Directory is not empty, stop cleaning up this path.
			return nil
		}

		// Directory is empty, attempt to remove it.
		if removeErr := os.Remove(currentDirToClean); removeErr != nil {
			if !os.IsNotExist(removeErr) { // Don\'t warn if already gone
				return fmt.Errorf("failed to remove empty export queue subdirectory %s: %w", currentDirToClean, removeErr)
			}
			// If os.IsNotExist, it means it was already removed or disappeared, which is fine.
			logger.Debug("Export queue subdirectory was already removed or disappeared",
				slog.String("directory", currentDirToClean))
		} else {
			logger.Debug("Successfully removed empty export queue subdirectory",
				slog.String("directory", currentDirToClean))
		}

		// Move to the parent directory for the next iteration.
		parent := filepath.Dir(currentDirToClean)
		if parent == currentDirToClean { // Safety break if at filesystem root
			return nil
		}
		currentDirToClean = parent
	}
}
