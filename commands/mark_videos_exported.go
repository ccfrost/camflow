package commands

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"

	"github.com/ccfrost/camflow/camflowconfig"
)

// MarkVideosExported moves videos from the video export queue to the exported directory.
// Unlike UploadVideos, this does not upload to Google Photos - it only organizes files locally.
// Videos are moved from export queue to exported dir.
func MarkVideosExported(ctx context.Context, config camflowconfig.CamflowConfig) error {
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
	itemsToMove, totalSize, err := scanExportQueue(exportQueueDir)
	if err != nil {
		return err
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
	bar := NewProgressBar(totalSize, "moving")

	for _, fileInfo := range itemsToMove {
		if _, err := moveToExported(&config.LocalVideos, fileInfo); err != nil {
			return fmt.Errorf("failed to move media item %s: %w", fileInfo.path, err)
		}
		bar.Add64(fileInfo.size)
	}

	_ = bar.Finish()
	fmt.Println() // End the progress bar line.

	logger.Debug("Finished moving videos to exported directory")
	return nil
}
