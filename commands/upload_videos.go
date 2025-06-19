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

	"github.com/ccfrost/camflow/camflowconfig"
	"github.com/gphotosuploader/google-photos-api-client-go/v3/media_items"
	"github.com/schollz/progressbar/v3"
	"golang.org/x/time/rate"
)

// videoFileInfo stores path and size for progress tracking.
type videoFileInfo struct {
	path string
	size int64
}

// UploadVideos uploads videos from the video export queue dir to Google Photos.
// Videos are added to all albums in config.DefaultAlbums.
// Uploaded videos are moved from export queue to VideosExportedRoot unless keepTargetRoot is true.
// The function is idempotent - if interrupted, it can be recalled to resume.
// Takes configDir to locate token and cache files, and a gphotosClient for API interaction.
func UploadVideos(ctx context.Context, config camflowconfig.CamediaConfig, cacheDirFlag string, keepTargetRoot bool, gphotosClient GPhotosClient) error {
	exportQueueDir := config.VideosExportQueueRoot
	if exportQueueDir == "" {
		return fmt.Errorf("video export queue directory (VideosExportQueueRoot) not configured")
	}

	if _, err := os.Stat(exportQueueDir); os.IsNotExist(err) {
		logger.Info("Video export queue directory does not exist, nothing to upload",
			slog.String("export_queue_dir", exportQueueDir))
		return nil
	}

	// --- Initialize Rate Limiter ---
	// Limit to 5 operations per second, allowing bursts of up to 10.
	// TODO: check the actual rate limits for Google Photos API.
	limiter := rate.NewLimiter(rate.Every(time.Second/5), 10)

	// List all video files in export queue, store path and size, calculate total size
	var videosToUpload []videoFileInfo
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

		// Assume all files in the video export queue dir are videos
		info, statErr := d.Info()
		if statErr != nil {
			return fmt.Errorf("failed to get file info for %s: %w", path, statErr)
		}
		videosToUpload = append(videosToUpload, videoFileInfo{path: path, size: info.Size()})
		totalSize += info.Size()
		return nil
	})
	if err != nil { // This error is from WalkDir itself, e.g. root dir not found.
		return fmt.Errorf("failed to walk video export queue dir '%s': %w", exportQueueDir, err)
	}
	if len(walkErrs) > 0 {
		logger.Warn("Encountered errors during directory walk, proceeding with successfully found files",
			slog.Int("error_count", len(walkErrs)))
	}

	if len(videosToUpload) == 0 {
		logger.Info("No videos found in export queue directory")
		return nil
	}
	logger.Info("Found videos to upload",
		slog.Int("count", len(videosToUpload)),
		slog.Float64("total_size_gb", math.Ceil(float64(totalSize)/1024/1024/1024)))

	// --- Get Album IDs (and create if they don't exist) ---
	if len(config.GooglePhotos.DefaultAlbums) == 0 {
		logger.Warn("No default albums specified in config, videos will only be uploaded to the library")
	}

	albumCachePath, err := getAlbumCachePath(cacheDirFlag)
	if err != nil {
		return fmt.Errorf("failed to get album cache path: %w", err)
	}
	albumCache, err := loadAlbumCache(albumCachePath)
	if err != nil {
		return fmt.Errorf("failed to load album cache: %w", err)
	}

	// Prepare map for albumID -> albumTitle for videos
	resolvedTargetAlbums := make(map[string]string)

	if len(config.GooglePhotos.DefaultAlbums) > 0 {
		// Filter out any empty album titles to avoid processing them
		var validAlbumTitles []string
		for _, title := range config.GooglePhotos.DefaultAlbums {
			if strings.TrimSpace(title) != "" {
				validAlbumTitles = append(validAlbumTitles, title)
			}
		}

		if len(validAlbumTitles) > 0 {
			var albumIDs []string
			albumIDs, err = albumCache.getOrFetchAndCreateAlbumIDs(ctx, gphotosClient.Albums(), validAlbumTitles, limiter)
			if err != nil {
				return fmt.Errorf("failed to resolve or create album IDs for titles %v: %w", validAlbumTitles, err)
			}

			if len(albumIDs) > 0 { // Only print if IDs were actually found/created
				logger.Debug("Target album IDs resolved/created",
					slog.Any("album_ids", albumIDs),
					slog.Any("titles", validAlbumTitles))
			}

			// Populate resolvedTargetAlbums, mapping ID to its corresponding Title
			// This assumes getOrFetchAndCreateAlbumIDs returns IDs in the same order as titles
			// and that all titles successfully resolve to an ID if no error is returned.
			for i, id := range albumIDs {
				if id != "" {
					resolvedTargetAlbums[id] = validAlbumTitles[i]
				}
			}
		} else if len(config.GooglePhotos.DefaultAlbums) > 0 {
			logger.Warn("DefaultAlbums list in config contains only empty or whitespace titles")
		}
	}
	// If resolvedTargetAlbums is empty at this point, videos are uploaded to library only.

	// --- Upload Loop ---

	bar := progressbar.DefaultBytes(
		totalSize,
		"Uploading videos",
	)

	for _, videoInfo := range videosToUpload {
		if err := uploadVideo(ctx, config, keepTargetRoot, gphotosClient, videoInfo.path, videoInfo.size, resolvedTargetAlbums, bar, limiter); err != nil {
			return err
		}
	}

	_ = bar.Finish() // Ignore error on finish

	logger.Debug("Video upload process finished")
	return nil
}

// uploadVideo uploads a single video "videoPath" of size "fileSize" to google photos.
// It updates "bar" with the bytes it has uploaded.
// It deletes the file after uploading if "keepTargetRoot" is false.
// "targetAlbumIDs" are the ids for DefaultAlbums in the config.
func uploadVideo(ctx context.Context, config camflowconfig.CamediaConfig, keepTargetRoot bool, gphotosClient GPhotosClient, videoPath string, fileSize int64, targetAlbums map[string]string, bar *progressbar.ProgressBar, limiter *rate.Limiter) error {
	if err := config.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	videoBasename := filepath.Base(videoPath)
	bar.Describe(fmt.Sprintf("Uploading %s", videoBasename))

	// Defer the progress bar update to ensure it happens once per file attempt.
	defer bar.Add64(fileSize)

	// Wait before uploading file
	if err := limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter error before uploading %s: %w", videoBasename, err)
	}

	// TODO: consider parallelizing uploads.
	// TODO: consider do resumable uploads.
	// TODO: consider updating progress bar with actual upload progress. (gphotos UploadFile calls NewUploadFromFile, which returns a file, so it is close.)
	uploadToken, err := gphotosClient.Uploader().UploadFile(ctx, videoPath)
	if err != nil {
		// TODO: only log error and skip? Want to make sure user notices.
		// fmt.Printf("\nError uploading file %s: %v. Skipping.\n", videoBasename, err)
		// return nil // Skip to the next video, progress bar will be updated by defer
		return fmt.Errorf("failed to upload file %s: %w", videoBasename, err)
	}

	// Wait before creating media item
	if err := limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter error before creating media item for %s: %w", videoBasename, err)
	}
	simpleMediaItem := media_items.SimpleMediaItem{
		UploadToken: uploadToken,
		Filename:    videoBasename,
	}
	// TODO: consider batching.
	mediaItem, err := gphotosClient.MediaItems().Create(ctx, simpleMediaItem)
	if err != nil {
		logger.Error("Error creating media item, skipping",
			slog.String("file", videoBasename),
			slog.String("token", uploadToken),
			slog.String("error", err.Error()))
		return nil // Skip to the next video, progress bar will be updated by defer
	}
	logger.Debug("Successfully created media item",
		slog.String("file", videoBasename),
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
				return fmt.Errorf("rate limiter error before adding %s to album %s: %w", videoBasename, albumTitle, err)
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
				slog.String("file", videoBasename),
				slog.Int("failed_count", len(failedAlbums)),
				slog.Any("failed_albums", failedAlbums))
		} else if addedCount > 0 {
			logger.Debug("Successfully added to all target albums",
				slog.String("file", videoBasename),
				slog.Int("album_count", addedCount))
		}
	}

	if !successfullyAddedToAll {
		if !keepTargetRoot {
			logger.Error("Video was not successfully added to all target albums, it will not be moved from export queue",
				slog.String("file", videoBasename))
		}
		return nil
	}

	if keepTargetRoot {
		logger.Debug("Keeping video in export queue directory as per keepTargetRoot flag",
			slog.String("file", videoPath))
		return nil
	}

	relPath, err := filepath.Rel(config.VideosExportQueueRoot, videoPath)
	if err != nil {
		return fmt.Errorf("failed to get relative path for %s from export queue root %s: %w", videoPath, config.VideosExportQueueRoot, err)
	}
	destPath := filepath.Join(config.VideosExportedRoot, relPath)
	destDir := filepath.Dir(destPath)

	// Check for collision at destination
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("failed to move %s: destination file %s already exists", videoPath, destPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to move %s: error checking destination %s: %w", videoPath, destPath, err)
	}

	logger.Debug("Moving video file",
		slog.String("from", videoPath),
		slog.String("to", destPath))
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory %s for moving %s: %w", destDir, videoPath, err)
	}

	// XXX: os.Rename requires source and destination to be on the same filesystem for atomic move.
	// If they are on different filesystems, it may fail or behave differently.
	if err := os.Rename(videoPath, destPath); err != nil {
		return fmt.Errorf("failed to move %s from export queue to %s: %w", videoPath, destPath, err)
	}
	logger.Debug("Successfully moved video file",
		slog.String("from", videoPath),
		slog.String("to", destPath))

	if err := cleanupEmptyTargetRootDirectories(videoPath, config.VideosExportQueueRoot); err != nil {
		// Log the error but don't cause uploadVideo to fail, as cleanup is secondary.
		logger.Error("Warning: cleanup of export queue directories failed",
			slog.String("video_path", videoPath),
			slog.String("error", err.Error()))
	}

	return nil
}

// cleanupEmptyTargetRootDirectories removes empty parent directories of the moved video file
// within the export queue area. It cleans directories from the video's original parent
// up to, but not including, the exportQueueDir.
// It returns an error if any unexpected issue occurs during the cleanup process.
func cleanupEmptyTargetRootDirectories(videoPath string, exportQueueRootPath string) error {
	// This logic cleans up empty parent directories of the moved video,
	// up to, but not including, the export queue root directory.
	cleanedTargetRootRoot := filepath.Clean(exportQueueRootPath)
	currentDirToClean := filepath.Clean(filepath.Dir(videoPath))

	// Loop to remove empty parent directories.
	// The loop stops if currentDirToClean is the export queue root,
	// is outside the export queue root, or is not empty.
	for {
		// Stop if currentDirToClean is no longer a strict subdirectory of cleanedTargetRootRoot,
		// or if it's the export queue root itself, or a root-like path.
		// We use strings.HasPrefix to ensure we are within the export queue root's path.
		// We also check currentDirToClean != cleanedTargetRootRoot to ensure we don't process the root itself.
		if !strings.HasPrefix(currentDirToClean, cleanedTargetRootRoot) ||
			currentDirToClean == cleanedTargetRootRoot ||
			currentDirToClean == "." ||
			currentDirToClean == string(os.PathSeparator) {
			return nil
		}

		// Ensure currentDirToClean is actually a child of cleanedTargetRootRoot, not just sharing a prefix.
		// e.g. /tmp/export-queue-other should not be processed if root is /tmp/export-queue.
		if !strings.HasPrefix(currentDirToClean, cleanedTargetRootRoot+string(os.PathSeparator)) {
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
			logger.Debug("TargetRoot subdirectory was already removed or disappeared",
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
