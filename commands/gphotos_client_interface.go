//go:generate go run github.com/golang/mock/mockgen -source=${GOFILE} -destination=zz_generated_local_mocks_test.go -package=commands GPhotosClient,AppAlbumsService,AppMediaItemsService
//go:generate go run github.com/golang/mock/mockgen -destination=mock_media_uploader_test.go -package=commands github.com/gphotosuploader/google-photos-api-client-go/v3 MediaUploader

package commands

import (
	"context"

	gphotosUploader "github.com/gphotosuploader/google-photos-api-client-go/v3"
	"github.com/gphotosuploader/google-photos-api-client-go/v3/albums"
	"github.com/gphotosuploader/google-photos-api-client-go/v3/media_items"
)

// GPhotosClient defines the interface for Google Photos client operations
// needed by the camedia commands.
type GPhotosClient interface {
	Albums() AppAlbumsService
	MediaItems() AppMediaItemsService
	Uploader() gphotosUploader.MediaUploader
}

// AppAlbumsService defines the interface for album-related operations we use.
type AppAlbumsService interface {
	List(ctx context.Context) ([]albums.Album, error)
	Create(ctx context.Context, title string) (*albums.Album, error)
	AddMediaItems(ctx context.Context, albumID string, mediaItemIDs []string) error
}

// AppMediaItemsService defines the interface for media item-related operations we use.
type AppMediaItemsService interface {
	Create(ctx context.Context, item media_items.SimpleMediaItem) (*media_items.MediaItem, error)
}

// The following interfaces are for types returned by the services,
// for which we also need mocks. They are defined by the external library.
// type MediaUploader interface { ... } // Provided by gphotosUploader.MediaUploader
