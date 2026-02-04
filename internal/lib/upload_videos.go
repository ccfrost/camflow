package lib

import (
	"context"
	"fmt"

	"github.com/ccfrost/camflow/internal/config"
)

// UploadVideos uploads videos from the video upload queue to Google Photos.
// Videos are added to Google Photos album named DefaultAlbum.
// Uploaded videos are moved from upload queue to uploaded dir; unless keepQueued is true, in which case they are copied (but not moved).
// The function is idempotent - if interrupted, it can be recalled to resume.
func UploadVideos(ctx context.Context, cfg config.CamflowConfig, cacheDirFlag string, keepQueued bool, gphotosClient GPhotosClient) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	return uploadMediaItems(ctx, cacheDirFlag, keepQueued, &cfg.LocalVideos, &cfg.GooglePhotos.Videos, "videos", gphotosClient)
}
