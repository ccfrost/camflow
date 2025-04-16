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

const chunkSize = 10 * 1024 * 1024 // 10MB chunks

// UploadVideos uploads videos from the staging video dir to Google Photos.
// Videos are added to all albums in config.DefaultAlbums.
// Uploaded videos are deleted from staging unless keepStaging is true.
// The function is idempotent - if interrupted, it can be recalled to resume.
func UploadVideos(ctx context.Context, config camediaconfig.CamediaConfig, keepStaging bool, client *googlephotos.Client) error {
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

	// Get all target albums
	albums := make(map[string]*googlephotos.Album)
	for _, albumTitle := range config.DefaultAlbums {
		album, err := client.GetAlbum(ctx, albumTitle)
		if err != nil {
			return fmt.Errorf("failed to get album %q: %w. Please ensure it exists.", albumTitle, err)
		}
		albums[albumTitle] = album
	}

	overallBar := progressbar.NewOptions(len(videos),
		progressbar.OptionSetDescription("uploading videos:"),
		progressbar.OptionSetWidth(20),
		progressbar.OptionShowCount(),
		progressbar.OptionSetPredictTime(true),
	)
	defer func() {
		if err := overallBar.Close(); err != nil {
			fmt.Printf("warning: failed to close progress bar\n")
		}
	}()

	fileBar := progressbar.NewOptions64(-1,
		progressbar.OptionSetDescription("current file:"),
		progressbar.OptionSetWidth(20),
		progressbar.OptionShowBytes(true),
		progressbar.OptionShowCount(),
		progressbar.OptionShowElapsedTimeOnFinish(),
		progressbar.OptionOnCompletion(func() { fmt.Println() }),
	)
	defer func() {
		if err := fileBar.Close(); err != nil {
			fmt.Printf("warning: failed to close file progress bar\n")
		}
	}()

	// Upload each video and add to albums
	for _, video := range videos {
		fileBar.Describe(fmt.Sprintf("uploading %s:", filepath.Base(video)))
		fileBar.Reset()

		// Upload video with progress tracking
		mediaItem, err := client.UploadVideo(ctx, video, func(p googlephotos.UploadProgress) {
			fileBar.ChangeMax64(p.TotalBytes)
			fileBar.Set64(p.BytesUploaded + int64(float64(chunkSize)*p.ChunkProgress))
		})
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

		overallBar.Add(1)
	}

	return nil
}
