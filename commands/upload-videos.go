package commands

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ccfrost/camedia/camediaconfig"
)

// UploadVideos uploads videos from the staging video dir to Google Photos.
// Videos are added to all albums in config.DefaultAlbums.
// Uploaded videos are deleted from staging unless keepStaging is true.
// The function is idempotent - if interrupted, it can be recalled to resume.
func UploadVideos(ctx context.Context, config camediaconfig.CamediaConfig, keepStaging bool) error {
	// Get staging directory
	stagingDir, err := videoStagingDir()
	if err != nil {
		return fmt.Errorf("failed to get video staging dir: %w", err)
	}

	// Check if staging dir exists and has files
	if _, err := os.Stat(stagingDir); os.IsNotExist(err) {
		// No videos to upload
		return nil
	}

	// List all video files in staging
	var videos []string
	err = filepath.WalkDir(stagingDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			ext := filepath.Ext(path)
			if ext == ".MP4" || ext == ".mp4" {
				videos = append(videos, path)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to list staged videos: %w", err)
	}

	if len(videos) == 0 {
		return nil
	}

	// TODO: Upload videos. Additional requirements:
	// Support resuming from a partial upload.
	// Add the video to each album in config.DefaultAlbums.
	// After uploading, delete the video from staging if keepStaging is false.
	// Show a progress bar for the total number of bytes to upload.

	return nil
}
