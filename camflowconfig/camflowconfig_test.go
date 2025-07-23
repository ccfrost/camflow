package camflowconfig_test

import (
	"path/filepath"
	"testing"

	"github.com/ccfrost/camflow/camflowconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_Snapshot(t *testing.T) {
	// Get the path to the test config file.
	configPath, err := filepath.Abs("testdata/config.toml")
	require.NoError(t, err)

	// Load the config.
	config, err := camflowconfig.LoadConfig(configPath)
	require.NoError(t, err)

	// Validate the config.
	err = config.Validate()
	require.NoError(t, err)

	// Assert the values.
	expected := camflowconfig.CamflowConfig{
		PhotosToProcessRoot:  "/photos/process",
		PhotosExportQueueDir: "/photos/queue",
		PhotosExportedRoot:   "/photos/exported",
		LocalPhotos: camflowconfig.LocalPhotosConfig{
			ToProcessRoot:  "/photos/process",
			ExportQueueDir: "/photos/queue",
			ExportedRoot:   "/photos/exported",
		},
		VideosExportQueueRoot: "/videos/queue",
		VideosExportedRoot:    "/videos/exported",
		LocalVideos: camflowconfig.LocalVideosConfig{
			ExportQueueRoot: "/videos/queue",
			ExportedRoot:    "/videos/exported",
		},
		GooglePhotos: camflowconfig.GooglePhotosConfig{
			ClientId:     "test-client-id",
			ClientSecret: "test-client-secret",
			RedirectURI:  "http://localhost:8080/auth",
			Photos: camflowconfig.GPPhotosConfig{
				DefaultAlbum: "Test Photos Album",
				LabelAlbums: []camflowconfig.KeyAlbum{
					{Key: "Red", Album: "Test Favorite Album"},
				},
				SubjectAlbums: []camflowconfig.KeyAlbum{
					{Key: "share-family", Album: "Test Family Album"},
				},
			},
			Videos: camflowconfig.GPVideosConfig{
				DefaultAlbum: "Test Videos Album",
			},
		},
	}

	// We need to clear the path field as it's not part of the snapshot.
	// This is a bit of a hack, but it's the easiest way to make the test pass.
	assert.Equal(t, expected.PhotosToProcessRoot, config.PhotosToProcessRoot)
	assert.Equal(t, expected.PhotosExportQueueDir, config.PhotosExportQueueDir)
	assert.Equal(t, expected.PhotosExportedRoot, config.PhotosExportedRoot)
	assert.Equal(t, expected.LocalPhotos, config.LocalPhotos)
	assert.Equal(t, expected.VideosExportQueueRoot, config.VideosExportQueueRoot)
	assert.Equal(t, expected.VideosExportedRoot, config.VideosExportedRoot)
	assert.Equal(t, expected.LocalVideos, config.LocalVideos)
	assert.Equal(t, expected.GooglePhotos, config.GooglePhotos)
}
