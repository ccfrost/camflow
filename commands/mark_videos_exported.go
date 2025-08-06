package commands

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"

	"github.com/ccfrost/camflow/camflowconfig"
	"github.com/schollz/progressbar/v3"
)

// MarkVideosExported moves videos from the video export queue to the exported directory.
// Unlike UploadVideos, this does not upload to Google Photos - it only organizes files locally.
// Videos are moved from export queue to exported dir; unless keepQueued is true, in which case they are copied.
func MarkVideosExported(ctx context.Context, config camflowconfig.CamflowConfig, keepQueued bool) error {
	if err := config.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	
	exportQueueDir := config.LocalVideos.GetExportQueueRoot()
	if _, err := os.Stat(exportQueueDir); os.IsNotExist(err) {
		logger.Info("Export queue directory does not exist, nothing to move",
			slog.String("export_queue_dir", exportQueueDir))
		return nil
	}

	// List all files in export queue, store path and size, calculate total size
	var itemsToMove []itemFileInfo
	var totalSize int64
	var walkErrs []error
	err := filepath.WalkDir(exportQueueDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// If the error is about the root exportQueueDir itself not existing, propagate it.
			if path == exportQueueDir && os.IsNotExist(err) {
				return fmt.Errorf("export queue directory '%s' disappeared or unreadable: %w", exportQueueDir, err)
			}
			// For other errors (e.g. permission on a sub-file/dir), log and try to continue.
			logger.Error("Error accessing path during walk, skipping",
				slog.String("path", path),
				slog.String("error", err.Error()))
			walkErrs = append(walkErrs, err)
			return nil // Continue walking
		}

		if d.IsDir() || d.Name() == ".DS_Store" {
			return nil
		}

		// Assume all other files in the export queue dir are media items to move.
		info, statErr := d.Info()
		if statErr != nil {
			return fmt.Errorf("failed to get file info for %s: %w", path, statErr)
		}
		itemsToMove = append(itemsToMove, itemFileInfo{path: path, size: info.Size(), modTime: info.ModTime()})
		totalSize += info.Size()
		return nil
	})
	if err != nil { // This error is from WalkDir itself, e.g. root dir not found.
		return fmt.Errorf("failed to walk export queue dir '%s': %w", exportQueueDir, err)
	}
	if len(walkErrs) > 0 {
		logger.Warn("Encountered errors during directory walk, proceeding with successfully found files",
			slog.Int("error_count", len(walkErrs)))
	}

	if len(itemsToMove) == 0 {
		logger.Info("No media items found in export queue directory",
			slog.String("export_queue_dir", exportQueueDir))
		return nil
	}
	logger.Info("Found files to move",
		slog.Int("count", len(itemsToMove)),
		slog.Float64("total_size_gb", math.Ceil(float64(totalSize)/1024/1024/1024)))

	// Move media items to exported directory with progress bar
	bar := progressbar.DefaultBytes(
		totalSize,
		"Moving videos to exported directory",
	)

	for _, fileInfo := range itemsToMove {
		if err := moveMediaItemToExported(keepQueued, &config.LocalVideos, fileInfo, bar); err != nil {
			return fmt.Errorf("failed to move media item %s: %w", fileInfo.path, err)
		}
	}

	_ = bar.Finish()

	logger.Debug("Finished moving videos to exported directory")
	return nil
}

// moveMediaItemToExported moves a single media item to the exported directory.
// It updates "bar" with the bytes it has processed.
// It moves the file unless "keepQueued" is true, in which case it copies.
func moveMediaItemToExported(keepQueued bool, localConfig LocalConfig, fileInfo itemFileInfo, bar *progressbar.ProgressBar) error {
	fileBasename := filepath.Base(fileInfo.path)
	bar.Describe(fmt.Sprintf("Moving %s", fileBasename))

	// Defer the progress bar update to ensure it happens once per file attempt.
	defer bar.Add64(fileInfo.size)

	if keepQueued {
		logger.Debug("Keeping file in export queue directory as per keepQueued flag (will copy instead of move)",
			slog.String("file", fileInfo.path))
	}

	year, month, day, err := parseDatePrefix(fileBasename)
	if err != nil {
		return fmt.Errorf("failed to parse date prefix from file name %s: %w", fileBasename, err)
	}
	relPath := filepath.Join(year, month, day, fileBasename)
	destPath := filepath.Join(localConfig.GetExportedRoot(), relPath)
	destDir := filepath.Dir(destPath)

	logger.Debug("Moving/copying file",
		slog.String("from", fileInfo.path),
		slog.String("to", destPath))
	
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory %s for moving %s: %w", destDir, fileInfo.path, err)
	}

	// Check if destination exists and remove it (overwrite behavior)
	if _, err := os.Stat(destPath); err == nil {
		logger.Debug("Destination file exists, removing it to overwrite",
			slog.String("dest", destPath))
		if err := os.Remove(destPath); err != nil {
			return fmt.Errorf("failed to remove existing destination file %s: %w", destPath, err)
		}
	}

	if keepQueued {
		// Copy the file, keeping the original
		if err := copyFile(fileInfo.path, destPath, fileInfo.size, fileInfo.modTime, nil /*bar*/); err != nil {
			return fmt.Errorf("failed to copy %s to %s: %w", fileInfo.path, destPath, err)
		}
	} else {
		// Move the file
		if sameFilesystem, err := isSameFilesystem(fileInfo.path, destDir); err != nil {
			return fmt.Errorf("failed to check if source and destination are on the same filesystem: %w", err)
		} else if sameFilesystem {
			if err := os.Rename(fileInfo.path, destPath); err != nil {
				return fmt.Errorf("failed to move %s from export queue to %s: %w", fileInfo.path, destPath, err)
			}
		} else {
			// Cross-filesystem move: copy then delete
			if err := copyFile(fileInfo.path, destPath, fileInfo.size, fileInfo.modTime, nil /*bar*/); err != nil {
				return fmt.Errorf("failed to copy %s to %s: %w", fileInfo.path, destPath, err)
			}
			if err := os.Remove(fileInfo.path); err != nil {
				return fmt.Errorf("failed to remove original file %s after copying to %s: %w", fileInfo.path, destPath, err)
			}
		}
	}
	
	logger.Debug("Successfully moved/copied file",
		slog.String("from", fileInfo.path),
		slog.String("to", destPath))

	return nil
}