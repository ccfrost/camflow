package commands

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gphotosuploader/google-photos-api-client-go/v3/media_items"
	"github.com/schollz/progressbar/v3"
	"golang.org/x/time/rate"
)

type LocalConfig interface {
	GetExportQueueRoot() string
	GetExportedRoot() string
	ExportQueueIsFlat() bool
}

type GPConfig interface {
	GetDefaultAlbum() string
}

// itemFileInfo stores path and size for progress tracking.
type itemFileInfo struct {
	path    string
	size    int64
	modTime time.Time
}

// uploadMediaItems uploads media items from the export queue dir to Google Photos.
// Media items are added to Google Photos album named DefaultAlbum.
// Uploaded media items are moved from export queue to exported dir; unless keepQueued is true, in which case they are copied (but not moved).
// The function is idempotent - if interrupted, it can be recalled to resume.
func uploadMediaItems(ctx context.Context, cacheDirFlag string, keepQueued bool, localConfig LocalConfig, gpConfig GPConfig, itemTypePluralName string, gphotosClient GPhotosClient) error {
	exportQueueDir := localConfig.GetExportQueueRoot()
	if _, err := os.Stat(exportQueueDir); os.IsNotExist(err) {
		logger.Info("Export queue directory does not exist, nothing to upload",
			slog.String("export_queue_dir", exportQueueDir))
		return nil
	}

	// --- Initialize Rate Limiter ---
	// Limit to 5 operations per second, allowing bursts of up to 10.
	// TODO: check the actual rate limits for Google Photos API.
	limiter := rate.NewLimiter(rate.Every(time.Second/5), 10)

	// List all files in export queue, store path and size, calculate total size
	var itemsToUpload []itemFileInfo
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

		if d.IsDir() {
			return nil
		}

		// Assume all files in the export queue dir are media items to upload.
		info, statErr := d.Info()
		if statErr != nil {
			return fmt.Errorf("failed to get file info for %s: %w", path, statErr)
		}
		itemsToUpload = append(itemsToUpload, itemFileInfo{path: path, size: info.Size(), modTime: info.ModTime()})
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

	if len(itemsToUpload) == 0 {
		logger.Info("No media items found in export queue directory")
		return nil
	}
	logger.Info("Found files to upload",
		slog.Int("count", len(itemsToUpload)),
		slog.Float64("total_size_gb", math.Ceil(float64(totalSize)/1024/1024/1024)))

	// --- Get Album IDs (and create if they don't exist) ---
	if gpConfig.GetDefaultAlbum() == "" {
		logger.Warn("No default albums specified in config, files will only be uploaded to the library")
	}

	albumCachePath, err := getAlbumCachePath(cacheDirFlag)
	if err != nil {
		return fmt.Errorf("failed to get album cache path: %w", err)
	}
	albumCache, err := loadAlbumCache(albumCachePath)
	if err != nil {
		return fmt.Errorf("failed to load album cache: %w", err)
	}

	// TODO: simplify the album lookup code now that there is only one default album.

	// Prepare map for albumID -> albumTitle.
	resolvedTargetAlbums := make(map[string]string)

	defaultAlbum := gpConfig.GetDefaultAlbum()
	if defaultAlbum != "" {
		if strings.TrimSpace(defaultAlbum) == "" {
			logger.Warn("DefaultAlbums list in config contains only empty or whitespace titles")
		} else {
			var albumIDs []string
			albumIDs, err = albumCache.getOrFetchAndCreateAlbumIDs(ctx, gphotosClient.Albums(), []string{defaultAlbum}, limiter)
			if err != nil {
				return fmt.Errorf("failed to resolve or create album IDs for title %s: %w", defaultAlbum, err)
			}

			if len(albumIDs) > 0 { // Only print if IDs were actually found/created
				logger.Debug("Target album IDs resolved/created",
					slog.Any("album_ids", albumIDs),
					slog.Any("title", defaultAlbum))
			}

			// Populate resolvedTargetAlbums, mapping ID to its corresponding Title
			// This assumes getOrFetchAndCreateAlbumIDs returns IDs in the same order as titles
			// and that all titles successfully resolve to an ID if no error is returned.
			for _, id := range albumIDs {
				if id != "" {
					resolvedTargetAlbums[id] = defaultAlbum
				}
			}
		}
	}
	// If resolvedTargetAlbums is empty at this point, files are uploaded to library only.

	// --- Upload Loop ---

	bar := progressbar.DefaultBytes(
		totalSize,
		fmt.Sprint("Uploading ", itemTypePluralName),
	)

	for _, fileInfo := range itemsToUpload {
		if err := uploadMediaItem(ctx, keepQueued, localConfig, gphotosClient, fileInfo, resolvedTargetAlbums, bar, limiter); err != nil {
			return err
		}
	}

	_ = bar.Finish() // Ignore error on finish

	logger.Debug(fmt.Sprint("Finished uploading ", itemTypePluralName))
	return nil
}

// uploadMediaItem uploads a single media item "filePath" of size "fileSize" to google photos.
// It updates "bar" with the bytes it has uploaded.
// It deletes the file after uploading if "keepQueued" is false.
// "targetAlbumIDs" are the ids for DefaultAlbums in the config.
func uploadMediaItem(ctx context.Context, keepQueued bool, localConfig LocalConfig, gphotosClient GPhotosClient, fileInfo itemFileInfo, targetAlbums map[string]string, bar *progressbar.ProgressBar, limiter *rate.Limiter) error {
	fileBasename := filepath.Base(fileInfo.path)
	bar.Describe(fmt.Sprintf("Uploading %s", fileBasename))

	// Defer the progress bar update to ensure it happens once per file attempt.
	defer bar.Add64(fileInfo.size)

	// Wait before uploading file
	if err := limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter error before uploading %s: %w", fileBasename, err)
	}

	// TODO: consider parallelizing uploads.
	// TODO: consider do resumable uploads.
	// TODO: consider updating progress bar with actual upload progress. (gphotos UploadFile calls NewUploadFromFile, which returns a file, so it is close.)
	uploadToken, err := gphotosClient.Uploader().UploadFile(ctx, fileInfo.path)
	if err != nil {
		// TODO: only log error and skip? Want to make sure user notices.
		// fmt.Printf("\nError uploading file %s: %v. Skipping.\n", fileBasename, err)
		// return nil // Skip to the next item, progress bar will be updated by defer
		return fmt.Errorf("failed to upload file %s: %w", fileBasename, err)
	}

	// Wait before creating media item
	if err := limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter error before creating media item for %s: %w", fileBasename, err)
	}
	simpleMediaItem := media_items.SimpleMediaItem{
		UploadToken: uploadToken,
		Filename:    fileBasename,
	}
	// TODO: consider batching.
	mediaItem, err := gphotosClient.MediaItems().Create(ctx, simpleMediaItem)
	if err != nil {
		logger.Error("Error creating media item, skipping",
			slog.String("file", fileBasename),
			slog.String("token", uploadToken),
			slog.String("error", err.Error()))
		return nil // Skip to the next item, progress bar will be updated by defer
	}
	logger.Debug("Successfully created media item",
		slog.String("file", fileBasename),
		slog.String("media_id", mediaItem.ID))

	// TODO: consider batch adding items to albums.
	successfullyAddedToAll := true
	if len(targetAlbums) > 0 {
		addedCount := 0
		var failedAlbums []string

		albumsService := gphotosClient.Albums()
		for albumID, albumTitle := range targetAlbums {
			// Wait before adding to album
			if err := limiter.Wait(ctx); err != nil {
				return fmt.Errorf("rate limiter error before adding %s to album %s: %w", fileBasename, albumTitle, err)
			}
			err = albumsService.AddMediaItems(ctx, albumID, []string{mediaItem.ID})
			if err != nil {
				logger.Error("Error adding media item to album",
					slog.String("media_id", mediaItem.ID),
					slog.String("album_title", albumTitle),
					slog.String("album_id", albumID),
					slog.String("error", err.Error()))
				failedAlbums = append(failedAlbums, albumTitle)
				successfullyAddedToAll = false
			} else {
				logger.Debug("Added media item to album",
					slog.String("media_id", mediaItem.ID),
					slog.String("album_title", albumTitle))
				addedCount++
			}
		}
		if len(failedAlbums) > 0 {
			logger.Error("Failed to add to some albums",
				slog.String("file", fileBasename),
				slog.Int("failed_count", len(failedAlbums)),
				slog.Any("failed_albums", failedAlbums))
		} else if addedCount > 0 {
			logger.Debug("Successfully added to all target albums",
				slog.String("file", fileBasename),
				slog.Int("album_count", addedCount))
		}
	}

	if !successfullyAddedToAll {
		if !keepQueued {
			logger.Error("File was not successfully added to all target albums, it will not be moved from export queue",
				slog.String("file", fileBasename))
		}
		return nil
	}

	if keepQueued {
		logger.Debug("Keeping file in export queue directory as per keepQueued flag",
			slog.String("file", fileInfo.path))
		return nil
	}

	var relPath string
	if localConfig.ExportQueueIsFlat() {
		year, month, day, err := parseDatePrefix(fileBasename)
		if err != nil {
			return fmt.Errorf("failed to parse date prefix from file name %s: %w", fileBasename, err)
		}
		relPath = filepath.Join(year, month, day, fileBasename)
	} else {
		var err error
		relPath, err = filepath.Rel(localConfig.GetExportQueueRoot(), fileInfo.path)
		if err != nil {
			return fmt.Errorf("failed to get relative path for %s from export queue root %s: %w", fileInfo.path, localConfig.GetExportQueueRoot(), err)
		}
	}
	destPath := filepath.Join(localConfig.GetExportedRoot(), relPath)
	destDir := filepath.Dir(destPath)

	// Check for collision at destination
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("failed to move %s: destination file %s already exists", fileInfo.path, destPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to move %s: error checking destination %s: %w", fileInfo.path, destPath, err)
	}

	logger.Debug("Moving file",
		slog.String("from", fileInfo.path),
		slog.String("to", destPath))
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory %s for moving %s: %w", destDir, fileInfo.path, err)
	}
	if sameFilesystem, err := isSameFilesystem(fileInfo.path, destDir); err != nil {
		return fmt.Errorf("failed to check if source and destination are on the same filesystem: %w", err)
	} else if sameFilesystem {
		if err := os.Rename(fileInfo.path, destPath); err != nil {
			return fmt.Errorf("failed to move %s from export queue to %s: %w", fileInfo.path, destPath, err)
		}
	} else {
		// TODO: clean up the possible .tmp file that could be left behind if this doesn't complete.
		if err := copyFile(fileInfo.path, destPath, fileInfo.size, fileInfo.modTime, nil /*bar*/); err != nil {
			return fmt.Errorf("failed to copy %s to %s: %w", fileInfo.path, destPath, err)
		}
		if err := os.Remove(fileInfo.path); err != nil {
			return fmt.Errorf("failed to remove original file %s after copying to %s: %w", fileInfo.path, destPath, err)
		}
	}
	logger.Debug("Successfully moved file",
		slog.String("from", fileInfo.path),
		slog.String("to", destPath))

	// TODO: this isn't quite idempotent, because if the rename happens and this doesn't, then rerunning uploadMediaItems won't see any files and so won't clean up the empty directories.
	// TODO: remove/disable this for photos?
	if err := cleanupEmptyParentDirs(fileInfo.path, localConfig.GetExportQueueRoot()); err != nil {
		// Log the error but don't cause uploadMediaItem to fail, as cleanup is secondary.
		logger.Error("Warning: cleanup of export queue directories failed",
			slog.String("file_path", fileInfo.path),
			slog.String("error", err.Error()))
	}

	return nil
}

// parseDatePrefix parses a basename "s" that is in the standard format of "YYYY-MM-DD-<rest-of-name>"
// and returns the year, month, and day parts.
func parseDatePrefix(s string) (year, month, day string, err error) {
	parts := strings.Split(s, "-")
	if len(parts) < 4 {
		return "", "", "", fmt.Errorf("invalid format: expected at least 4 parts separated by '-'")
	}
	if len(parts[0]) != 4 {
		return "", "", "", fmt.Errorf("invalid format: year '%s' must be 4 characters long", parts[0])
	}
	if len(parts[1]) != 2 {
		return "", "", "", fmt.Errorf("invalid format: month '%s' must be 2 characters long", parts[1])
	}
	if len(parts[2]) != 2 {
		return "", "", "", fmt.Errorf("invalid format: day '%s' must be 2 characters long", parts[2])
	}

	return parts[0], parts[1], parts[2], nil
}

// findExistingParent recursively walks up the directory tree to find a parent that exists.
func findExistingParent(rawPath string) (string, error) {
	absPath, err := filepath.Abs(rawPath)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path for %s: %w", rawPath, err)
	}

	current := absPath
	for {
		_, err := os.Stat(current)
		if err == nil {
			// Found a path that exists.
			return current, nil
		}
		if !os.IsNotExist(err) {
			// Some other error (eg, permission).
			return "", fmt.Errorf("error checking path %s: %w", current, err)
		}

		// Move to parent directory.
		parent := filepath.Dir(current)
		if parent == current {
			// Reached filesystem root - use it since any file created must be on this filesystem.
			return current, nil
		}
		current = parent
	}
}

// IsSameFileSystem_ForceFalse is a test-only variable that forces isSameFilesystem to return false.
// This allows testing cross-filesystem behavior even when source and destination are on the same filesystem.
var IsSameFileSystemForTests_ForceFalse bool

// isSameFilesystem checks if two paths are on the same filesystem.
// Handles cases where the paths don't exist yet by checking their existing parent directories.
func isSameFilesystem(path1, path2 string) (bool, error) {
	if IsSameFileSystemForTests_ForceFalse {
		return false, nil
	}

	existingPath1, err := findExistingParent(path1)
	if err != nil {
		return false, fmt.Errorf("failed to find existing parent for %s: %w", path1, err)
	}
	existingPath2, err := findExistingParent(path2)
	if err != nil {
		return false, fmt.Errorf("failed to find existing parent for %s: %w", path2, err)
	}

	// Note: if Stat fails because the path is a dangling symlink this returns an error.
	// That's okay because a later mkdir of that path would fail (I think), so there'd be
	// some work to do to support that case.
	stat1, err := os.Stat(existingPath1)
	if err != nil {
		return false, fmt.Errorf("failed to stat %s: %w", existingPath1, err)
	}
	stat2, err := os.Stat(existingPath2)
	if err != nil {
		return false, fmt.Errorf("failed to stat %s: %w", existingPath2, err)
	}

	stat1Sys, ok1 := stat1.Sys().(*syscall.Stat_t)
	stat2Sys, ok2 := stat2.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		return false, fmt.Errorf("unable to get filesystem device information for one/both of %s (%t) and %s (%t)", existingPath1, ok1, existingPath2, ok2)
	}
	return stat1Sys.Dev == stat2Sys.Dev, nil
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
