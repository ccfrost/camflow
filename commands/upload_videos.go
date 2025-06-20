package commands

import (
	"context"
	"fmt"

	"github.com/ccfrost/camflow/camflowconfig"
)

// UploadVideos uploads videos from the video export queue to Google Photos.
// Videos are added to Google Photos album named DefaultAlbum.
// Uploaded videos are moved from export queue to exported dir; unless keepQueued is true, in which case they are copied (but not moved).
// The function is idempotent - if interrupted, it can be recalled to resume.
func UploadVideos(ctx context.Context, config camflowconfig.CamflowConfig, cacheDirFlag string, keepQueued bool, gphotosClient GPhotosClient) error {
	if err := config.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	return uploadMediaItems(ctx, cacheDirFlag, keepQueued, &config.LocalVideos, &config.GooglePhotos.Videos, "videos", gphotosClient)
}
