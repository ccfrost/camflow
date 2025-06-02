package commands

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ccfrost/camedia/camediaconfig"
	"github.com/gphotosuploader/google-photos-api-client-go/v3/media_items"
	"github.com/schollz/progressbar/v3"
	"golang.org/x/time/rate"
)

// videoFileInfo stores path and size for progress tracking.
type videoFileInfo struct {
	path string
	size int64
}

// UploadVideos uploads videos from the staging video dir to Google Photos.
// Videos are added to all albums in config.DefaultAlbums.
// Uploaded videos are moved from staging to VideosOrigRoot unless keepStaging is true.
// The function is idempotent - if interrupted, it can be recalled to resume.
// Takes configDir to locate token and cache files, and a gphotosClient for API interaction.
func UploadVideos(ctx context.Context, config camediaconfig.CamediaConfig, cacheDirFlag string, keepStaging bool, gphotosClient GPhotosClient) error {
	// Get staging directory
	stagingDir := config.VideosOrigStagingRoot
	if stagingDir == "" {
		return fmt.Errorf("video staging directory (VideosOrigStagingRoot) not configured")
	}

	if _, err := os.Stat(stagingDir); os.IsNotExist(err) {
		fmt.Printf("Video staging directory '%s' does not exist, nothing to upload.\n", stagingDir)
		return nil
	}

	// --- Initialize Rate Limiter ---
	// Limit to 5 operations per second, allowing bursts of up to 10.
	// TODO: check the actual rate limits for Google Photos API.
	limiter := rate.NewLimiter(rate.Every(time.Second/5), 10)

	// List all video files in staging, store path and size, calculate total size
	var videosToUpload []videoFileInfo
	var totalSize int64
	var walkErrs []error
	err := filepath.WalkDir(stagingDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// If the error is about the root stagingDir itself not existing, propagate it.
			if path == stagingDir && os.IsNotExist(err) {
				return fmt.Errorf("staging directory '%s' disappeared or unreadable: %w", stagingDir, err)
			}
			// For other errors (e.g. permission on a sub-file/dir), log and try to continue.
			fmt.Printf("Warning: Error accessing path %s during walk: %v. Skipping.\n", path, err)
			walkErrs = append(walkErrs, err)
			return nil // Continue walking
		}

		if d.IsDir() {
			return nil
		}

		// Assume all files in the video staging dir are videos
		info, statErr := d.Info()
		if statErr != nil {
			return fmt.Errorf("failed to get file info for %s: %w", path, statErr)
		}
		videosToUpload = append(videosToUpload, videoFileInfo{path: path, size: info.Size()})
		totalSize += info.Size()
		return nil
	})
	if err != nil { // This error is from WalkDir itself, e.g. root dir not found.
		return fmt.Errorf("failed to walk video staging dir '%s': %w", stagingDir, err)
	}
	if len(walkErrs) > 0 {
		fmt.Printf("Encountered %d errors during directory walk. Proceeding with successfully found files.\n", len(walkErrs))
	}

	if len(videosToUpload) == 0 {
		fmt.Println("No videos found in staging directory.")
		return nil
	}
	fmt.Printf("Found %d videos to upload (total size: %.1f GB).\n", len(videosToUpload), float64(totalSize)/1024/1024/1024)

	// --- Get Album IDs (and create if they don't exist) ---
	if len(config.GooglePhotos.DefaultAlbums) == 0 {
		fmt.Println("Warning: No default albums specified in config. Videos will only be uploaded to the library.")
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
				fmt.Printf("Target album IDs resolved/created: %v for titles: %v\n", albumIDs, validAlbumTitles)
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
			fmt.Println("Warning: DefaultAlbums list in config contains only empty or whitespace titles.")
		}
	}
	// If resolvedTargetAlbums is empty at this point, videos are uploaded to library only.

	// --- Upload Loop ---

	bar := progressbar.DefaultBytes(
		totalSize,
		"Uploading videos",
	)

	for _, videoInfo := range videosToUpload {
		if err := uploadVideo(ctx, config, keepStaging, gphotosClient, videoInfo.path, videoInfo.size, resolvedTargetAlbums, bar, limiter); err != nil {
			return err
		}
	}

	_ = bar.Finish() // Ignore error on finish

	fmt.Println("\nVideo upload process finished.")
	return nil
}

// uploadVideo uploads a single video "videoPath" of size "fileSize" to google photos.
// It updates "bar" with the bytes it has uploaded.
// It deletes the file after uploading if "keepStaging" is false.
// "targetAlbumIDs" are the ids for DefaultAlbums in the config.
func uploadVideo(ctx context.Context, config camediaconfig.CamediaConfig, keepStaging bool, gphotosClient GPhotosClient, videoPath string, fileSize int64, targetAlbums map[string]string, bar *progressbar.ProgressBar, limiter *rate.Limiter) error {
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
		fmt.Printf("\nError creating media item for %s (token: %s): %v. Skipping.\n", videoBasename, uploadToken, err)
		return nil // Skip to the next video, progress bar will be updated by defer
	}
	fmt.Printf("\nSuccessfully created media item for %s (ID: %s)\n", videoBasename, mediaItem.ID)

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
				fmt.Printf("Error adding media item %s to album '%s' (ID: %s): %v\n", mediaItem.ID, albumTitle, albumID, err)
				failedAlbums = append(failedAlbums, albumTitle)
				successfullyAddedToAll = false
			} else {
				fmt.Printf("Added media item %s to album '%s'\n", mediaItem.ID, albumTitle)
				addedCount++
			}
		}
		if len(failedAlbums) > 0 {
			fmt.Printf("Warning: Failed to add %s to %d albums: %v\n", videoBasename, len(failedAlbums), failedAlbums)
		} else if addedCount > 0 {
			fmt.Printf("Successfully added %s to all %d target albums.\n", videoBasename, addedCount)
		}
	}

	if !successfullyAddedToAll {
		if !keepStaging {
			fmt.Printf("Warning: %s was not successfully added to all target albums. It will not be moved from staging.\n", videoBasename)
		}
		return nil
	}

	if keepStaging {
		fmt.Printf("\nKeeping %s in staging directory as per keepStaging flag.\n", videoPath)
		return nil
	}

	relPath, err := filepath.Rel(config.VideosOrigStagingRoot, videoPath)
	if err != nil {
		return fmt.Errorf("failed to get relative path for %s from staging root %s: %w", videoPath, config.VideosOrigStagingRoot, err)
	}
	destPath := filepath.Join(config.VideosOrigRoot, relPath)
	destDir := filepath.Dir(destPath)

	// Check for collision at destination
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("failed to move %s: destination file %s already exists", videoPath, destPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to move %s: error checking destination %s: %w", videoPath, destPath, err)
	}

	fmt.Printf("Moving %s to %s...\n", videoPath, destPath)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory %s for moving %s: %w", destDir, videoPath, err)
	}

	// XXX: os.Rename requires source and destination to be on the same filesystem for atomic move.
	// If they are on different filesystems, it may fail or behave differently.
	if err := os.Rename(videoPath, destPath); err != nil {
		return fmt.Errorf("failed to move %s from staging to %s: %w", videoPath, destPath, err)
	}
	fmt.Printf("Successfully moved %s to %s\n", videoPath, destPath)

	if err := cleanupEmptyStagingDirectories(videoPath, config.VideosOrigStagingRoot); err != nil {
		// Log the error but don't cause uploadVideo to fail, as cleanup is secondary.
		fmt.Printf("Warning: cleanup of staging directories for %s failed: %v\n", videoPath, err)
	}

	return nil
}

// cleanupEmptyStagingDirectories removes empty parent directories of the moved video file
// within the staging area. It cleans directories from the video's original parent
// up to, but not including, the stagingRootPath.
// It returns an error if any unexpected issue occurs during the cleanup process.
func cleanupEmptyStagingDirectories(videoPath string, stagingRootPath string) error {
	// This logic cleans up empty parent directories of the moved video,
	// up to, but not including, the staging root directory.
	cleanedStagingRoot := filepath.Clean(stagingRootPath)
	currentDirToClean := filepath.Clean(filepath.Dir(videoPath))

	// Loop to remove empty parent directories.
	// The loop stops if currentDirToClean is the staging root,
	// is outside the staging root, or is not empty.
	for {
		// Stop if currentDirToClean is no longer a strict subdirectory of cleanedStagingRoot,
		// or if it's the staging root itself, or a root-like path.
		// We use strings.HasPrefix to ensure we are within the staging root's path.
		// We also check currentDirToClean != cleanedStagingRoot to ensure we don't process the root itself.
		if !strings.HasPrefix(currentDirToClean, cleanedStagingRoot) ||
			currentDirToClean == cleanedStagingRoot ||
			currentDirToClean == "." ||
			currentDirToClean == string(os.PathSeparator) {
			return nil
		}

		// Ensure currentDirToClean is actually a child of cleanedStagingRoot, not just sharing a prefix.
		// e.g. /tmp/staging-other should not be processed if root is /tmp/staging
		if !strings.HasPrefix(currentDirToClean, cleanedStagingRoot+string(os.PathSeparator)) {
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
		fmt.Printf("Attempting to remove empty staging subdirectory: %s\n", currentDirToClean)
		if removeErr := os.Remove(currentDirToClean); removeErr != nil {
			if !os.IsNotExist(removeErr) { // Don\'t warn if already gone
				return fmt.Errorf("failed to remove empty staging subdirectory %s: %w", currentDirToClean, removeErr)
			}
			// If os.IsNotExist, it means it was already removed or disappeared, which is fine.
			fmt.Printf("Staging subdirectory %s was already removed or disappeared.\n", currentDirToClean)
		} else {
			fmt.Printf("Successfully removed empty staging subdirectory: %s\n", currentDirToClean)
		}

		// Move to the parent directory for the next iteration.
		parent := filepath.Dir(currentDirToClean)
		if parent == currentDirToClean { // Safety break if at filesystem root
			return nil
		}
		currentDirToClean = parent
	}
}
