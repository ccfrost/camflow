package commands

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ccfrost/camedia/camediaconfig"
	gphotos "github.com/gphotosuploader/google-photos-api-client-go/v3"
	"github.com/gphotosuploader/google-photos-api-client-go/v3/media_items"
	"github.com/schollz/progressbar/v3"
)

// videoFileInfo stores path and size for progress tracking.
type videoFileInfo struct {
	path string
	size int64
}

// UploadVideos uploads videos from the staging video dir to Google Photos.
// Videos are added to all albums in config.DefaultAlbums.
// Uploaded videos are deleted from staging unless keepStaging is true.
// The function is idempotent - if interrupted, it can be recalled to resume.
// Takes configDir to locate token and cache files, and a gphotosClient for API interaction.
func UploadVideos(ctx context.Context, config camediaconfig.CamediaConfig, configDir string, keepStaging bool, gphotosClient gphotos.Client) error {
	// Get staging directory
	stagingDir, err := videoStagingDir()
	if err != nil {
		return fmt.Errorf("failed to get video staging dir: %w", err)
	}

	// Check if staging dir exists
	if _, err := os.Stat(stagingDir); os.IsNotExist(err) {
		fmt.Println("Video staging directory does not exist, nothing to upload.")
		return nil
	}

	// List all video files in staging, store path and size, calculate total size
	var videosToUpload []videoFileInfo // Changed from []string
	var totalSize int64
	err = filepath.WalkDir(stagingDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err // Propagate errors during walk
		}
		if !d.IsDir() {
			// Basic check for common video extensions - adjusted to include MOV
			ext := filepath.Ext(path)
			// Convert to uppercase for case-insensitive comparison
			upperExt := strings.ToUpper(ext)
			if upperExt == ".MP4" || upperExt == ".MOV" {
				info, statErr := d.Info()
				if statErr != nil {
					fmt.Printf("Warning: Could not get file info for %s: %v\n", path, statErr)
					return nil // Continue walking, skip this file
				}
				// Store path and size
				videosToUpload = append(videosToUpload, videoFileInfo{path: path, size: info.Size()})
				totalSize += info.Size()
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to list staged videos: %w", err)
	}

	if len(videosToUpload) == 0 { // Check the new slice
		fmt.Println("No videos found in staging directory.")
		return nil
	}

	fmt.Printf("Found %d videos to upload (total size: %.2f MB).\n", len(videosToUpload), float64(totalSize)/1024/1024) // Use len of new slice

	// --- Get Album IDs ---
	if len(config.DefaultAlbums) == 0 {
		fmt.Println("Warning: No default albums specified in config. Videos will only be uploaded to the library.")
	}

	albumCachePath, err := getAlbumCachePath(configDir)
	if err != nil {
		return fmt.Errorf("failed to get album cache path: %w", err)
	}
	cache, err := loadAlbumCache(albumCachePath)
	if err != nil {
		return fmt.Errorf("failed to load album cache: %w", err)
	}

	var targetAlbumIDs []string
	if len(config.DefaultAlbums) > 0 {
		targetAlbumIDs, err = cache.getOrFetchAlbumIDs(ctx, gphotosClient.Albums, config.DefaultAlbums)
		if err != nil {
			return fmt.Errorf("failed to resolve album IDs: %w", err) // Error includes unfound albums
		}
		fmt.Printf("Target album IDs resolved: %v\n", targetAlbumIDs)
	}

	// --- Upload Loop ---
	bar := progressbar.DefaultBytes(
		totalSize,
		"Uploading videos",
	)

	// Iterate over the struct slice
	for _, videoInfo := range videosToUpload {
		videoPath := videoInfo.path // Get path from struct
		fileSize := videoInfo.size  // Get size from struct
		filename := filepath.Base(videoPath)
		bar.Describe(fmt.Sprintf("Uploading %s", filename))

		// 1. Upload bytes using the client's uploader
		uploadToken, err := gphotosClient.Uploader.UploadFile(ctx, videoPath)
		if err != nil {
			// Don't stop the whole process, just log and continue
			fmt.Printf("\nError uploading file %s: %v. Skipping.\n", filename, err)
			// Use the stored size for progress update on failure
			bar.Add64(fileSize)
			continue // Skip to the next video
		}

		// 2. Create Media Item using the upload token
		// Construct the SimpleMediaItem required by the Create method
		simpleMediaItem := media_items.SimpleMediaItem{
			UploadToken: uploadToken,
			Filename:    filename,
		}
		// Pass the struct instead of individual arguments
		mediaItem, err := gphotosClient.MediaItems.Create(ctx, simpleMediaItem)
		if err != nil {
			fmt.Printf("\nError creating media item for %s (token: %s): %v. Skipping.\n", filename, uploadToken, err)
			// Use the stored size for progress update on failure
			bar.Add64(fileSize)
			continue
		}
		// Corrected: mediaItem.Id -> mediaItem.ID
		fmt.Printf("\nSuccessfully created media item for %s (ID: %s)\n", filename, mediaItem.ID)

		// 3. Add Media Item to Albums (if any specified)
		successfullyAddedToAll := true // Assume success unless an error occurs
		if len(targetAlbumIDs) > 0 {
			addedCount := 0
			failedAlbums := []string{}
			// Create a map for quick lookup of album titles by ID
			albumIDToTitle := make(map[string]string)
			for i, id := range targetAlbumIDs {
				if i < len(config.DefaultAlbums) { // Safety check
					albumIDToTitle[id] = config.DefaultAlbums[i]
				}
			}

			for _, albumID := range targetAlbumIDs {
				albumTitle := albumIDToTitle[albumID] // Get title for logging
				if albumTitle == "" {
					albumTitle = albumID
				} // Fallback to ID if title not found

				// Corrected: mediaItem.Id -> mediaItem.ID
				err = gphotosClient.Albums.AddMediaItems(ctx, albumID, []string{mediaItem.ID})
				if err != nil {
					// Corrected: mediaItem.Id -> mediaItem.ID
					fmt.Printf("Error adding media item %s to album '%s' (ID: %s): %v\n", mediaItem.ID, albumTitle, albumID, err)
					failedAlbums = append(failedAlbums, albumTitle)
					successfullyAddedToAll = false // Mark as failed
				} else {
					// Corrected: mediaItem.Id -> mediaItem.ID
					fmt.Printf("Added media item %s to album '%s'\n", mediaItem.ID, albumTitle)
					addedCount++
				}
			}
			if len(failedAlbums) > 0 {
				fmt.Printf("Warning: Failed to add %s to %d albums: %v\n", filename, len(failedAlbums), failedAlbums)
				// Decide if this is a critical failure. For now, we continue but don't delete.
			} else {
				fmt.Printf("Successfully added %s to all %d target albums.\n", filename, addedCount)
			}
		}

		// 4. Delete from staging if required and successful
		if successfullyAddedToAll && !keepStaging {
			fmt.Printf("Deleting %s from staging...\n", videoPath)
			if err := os.Remove(videoPath); err != nil {
				// Log error but don't fail the whole process
				fmt.Printf("Error deleting %s from staging: %v\n", videoPath, err)
			}
		} else if !successfullyAddedToAll {
			fmt.Printf("Skipping deletion of %s due to failure adding to some albums.\n", videoPath)
		} else if keepStaging {
			fmt.Printf("Keeping %s in staging directory.\n", videoPath)
		}

		// Update progress bar after processing the file (even if deletion failed)
		// Use the stored size directly
		bar.Add64(fileSize)

	} // End of video loop

	// Ensure progress bar finishes cleanly
	_ = bar.Finish() // Ignore error on finish

	fmt.Println("\nVideo upload process finished.")
	return nil
}
