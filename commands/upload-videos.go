package commands

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ccfrost/camedia/camediaconfig"
	"github.com/ccfrost/camedia/commands/googlephotos"
	"github.com/schollz/progressbar/v3"
)

// UploadVideos uploads videos from the staging video dir to Google Photos.
// Videos are added to all albums in config.DefaultAlbums.
// Uploaded videos are deleted from staging unless keepStaging is true.
// The function is idempotent - if interrupted, it can be recalled to resume.
func UploadVideos(config camediaconfig.CamediaConfig, keepStaging bool) error {
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

	// Initialize Google Photos client
	ctx := context.Background()
	client, err := googlephotos.NewClient(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to create Google Photos client: %w", err)
	}

	// Create/get all target albums
	albums := make(map[string]*googlephotos.Album)
	for _, albumTitle := range config.DefaultAlbums {
		album, err := client.GetOrCreateAlbum(ctx, albumTitle)
		if err != nil {
			return fmt.Errorf("failed to get/create album %q: %w", albumTitle, err)
		}
		albums[albumTitle] = album
	}

	// Create progress bar
	bar := progressbar.NewOptions(len(videos),
		progressbar.OptionSetDescription("uploading videos:"),
		progressbar.OptionSetWidth(20),
		progressbar.OptionShowCount(),
		progressbar.OptionSetPredictTime(true),
	)

	// Upload each video and add to albums
	for _, video := range videos {
		// Upload video
		mediaItem, err := client.UploadVideo(ctx, video)
		if err != nil {
			return fmt.Errorf("failed to upload video %q: %w", video, err)
		}

		// Add to all albums
		for _, album := range albums {
			if err := client.AddMediaItemToAlbum(ctx, mediaItem, album); err != nil {
				return fmt.Errorf("failed to add video to album %q: %w", album.Title, err)
			}
		}

		// Delete from staging if requested
		if !keepStaging {
			if err := os.Remove(video); err != nil {
				return fmt.Errorf("failed to delete video from staging %q: %w", video, err)
			}
		}

		bar.Add(1)
	}

	if err := bar.Close(); err != nil {
		fmt.Printf("warning: failed to close progress bar\n")
	}

	return nil
}
