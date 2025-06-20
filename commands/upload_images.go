package commands

import (
	"context"
	"fmt"

	"github.com/ccfrost/camflow/camflowconfig"
)

// UploadPhotos uploads photos from the photo export queue dir to Google Photos.
// Photos are added to Google Photos album named DefaultAlbum.
// Uploaded photos are moved from export queue to exported dir; unless keepQueued is true, in which case they are copied (but not moved).
// The function is idempotent - if interrupted, it can be recalled to resume.
func UploadPhotos(ctx context.Context, config camflowconfig.CamflowConfig, cacheDirFlag string, keepQueued bool, gphotosClient GPhotosClient) error {
	if err := config.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	return uploadMediaItems(ctx, cacheDirFlag, keepQueued, &config.LocalPhotos, &config.GooglePhotos.Photos, "photos", gphotosClient)
}
