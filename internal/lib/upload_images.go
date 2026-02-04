package lib

import (
	"context"
	"fmt"

	"github.com/ccfrost/camflow/internal/config"
)

// UploadPhotos uploads photos from the photo upload queue dir to Google Photos.
// Photos are added to Google Photos album named DefaultAlbum.
// Uploaded photos are moved from upload queue to uploaded dir; unless keepQueued is true, in which case they are copied (but not moved).
// The function is idempotent - if interrupted, it can be recalled to resume.
func UploadPhotos(ctx context.Context, cfg config.CamflowConfig, cacheDirFlag string, keepQueued bool, gphotosClient GPhotosClient) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	return uploadMediaItems(ctx, cacheDirFlag, keepQueued, &cfg.LocalPhotos, &cfg.GooglePhotos.Photos, "photos", gphotosClient)
}
