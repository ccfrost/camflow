package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ccfrost/camflow/camflowconfig"
	"github.com/stretchr/testify/require"
)

func newTestConfig(t *testing.T, defaultAlbums []string) camflowconfig.CamediaConfig {
	t.Helper()

	tempDir := t.TempDir()
	c := camflowconfig.CamediaConfig{
		PhotosToProcessRoot:         filepath.Join(tempDir, "PhotosToProcessRoot"),
		PhotosExportQueueDir: filepath.Join(tempDir, "PhotosExportQueueDir"),
		PhotosExportedRoot:        filepath.Join(tempDir, "PhotosExportedRoot"),

		VideosExportQueueRoot: filepath.Join(tempDir, "VideosExportQueueRoot"),
		VideosExportedRoot:        filepath.Join(tempDir, "VideosExportedRoot"),

		GooglePhotos: camflowconfig.GooglePhotosConfig{
			ClientId:     "test-client-id",
			ClientSecret: "test-client-secret",
			RedirectURI:  "test-redirect-uri",

			DefaultAlbums: defaultAlbums,

			// Does not set ToFav or KeywordAlbums fields.
		},
	}
	for _, dir := range []string{
		c.PhotosToProcessRoot,
		c.PhotosExportQueueDir,
		c.PhotosExportedRoot,
		c.VideosExportQueueRoot,
		c.VideosExportedRoot,
	} {
		require.NoError(t, os.MkdirAll(dir, 0755), dir)
	}
	return c
}
