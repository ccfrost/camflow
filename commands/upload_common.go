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
	path string
	size int64
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
		itemsToUpload = append(itemsToUpload, itemFileInfo{path: path, size: info.Size()})
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

	// XXX: os.Rename requires source and destination to be on the same filesystem for atomic move.
	// If they are on different filesystems, it may fail or behave differently.
	if err := os.Rename(fileInfo.path, destPath); err != nil {
		return fmt.Errorf("failed to move %s from export queue to %s: %w", fileInfo.path, destPath, err)
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
