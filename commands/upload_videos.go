package commands

import (
	"context"
	"fmt"

	"github.com/ccfrost/camflow/camflowconfig"
)

// UploadVideos uploads videos from the video export queue dir to Google Photos.
// Videos are added to all albums in config.DefaultAlbums.
// Uploaded videos are moved from export queue to VideosExportedRoot unless keepQueued is true.
// The function is idempotent - if interrupted, it can be recalled to resume.
// Takes configDir to locate token and cache files, and a gphotosClient for API interaction.
func UploadVideos(ctx context.Context, config camflowconfig.CamflowConfig, cacheDirFlag string, keepQueued bool, gphotosClient GPhotosClient) error {
	if err := config.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	return uploadMediaItems(ctx, cacheDirFlag, keepQueued, &config.LocalVideos, &config.GooglePhotos.Videos, "videos", gphotosClient)
}
