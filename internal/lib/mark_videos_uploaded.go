package lib

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"

	"github.com/ccfrost/camflow/internal/config"
)

// MarkVideosUploaded moves videos from the video upload queue to the uploaded directory.
// Unlike UploadVideos, this does not upload to Google Photos - it only organizes files locally.
// Videos are moved from upload queue to uploaded dir.
func MarkVideosUploaded(ctx context.Context, cfg config.CamflowConfig) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	uploadQueueDir := cfg.LocalVideos.GetUploadQueueRoot()
	if _, err := os.Stat(uploadQueueDir); os.IsNotExist(err) {
		logger.Info("Upload queue directory does not exist, nothing to move",
			slog.String("upload_queue_dir", uploadQueueDir))
		return nil
	}

	// List all files in upload queue, store path and size, calculate total size
	itemsToMove, totalSize, err := scanUploadQueue(uploadQueueDir)
	if err != nil {
		return err
	}

	if len(itemsToMove) == 0 {
		logger.Info("No media items found in upload queue directory",
			slog.String("upload_queue_dir", uploadQueueDir))
		return nil
	}
	logger.Info("Found files to move",
		slog.Int("count", len(itemsToMove)),
		slog.Float64("total_size_gb", math.Ceil(float64(totalSize)/1024/1024/1024)))

	// Move media items to uploaded directory with progress bar
	bar := NewProgressBar(totalSize, "moving")

	for _, fileInfo := range itemsToMove {
		if _, err := moveToUploaded(&cfg.LocalVideos, fileInfo); err != nil {
			return fmt.Errorf("failed to move media item %s: %w", fileInfo.path, err)
		}
		bar.Add64(fileInfo.size)
	}

	_ = bar.Finish()
	fmt.Println() // End the progress bar line.

	logger.Debug("Finished moving videos to uploaded directory")
	return nil
}
