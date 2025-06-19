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
		PhotosOrigRoot:         filepath.Join(tempDir, "PhotosOrigRoot"),
		PhotosExportStagingDir: filepath.Join(tempDir, "PhotosExportStagingDir"),
		PhotosExportDir:        filepath.Join(tempDir, "PhotosExportDir"),

		VideosOrigStagingRoot: filepath.Join(tempDir, "VideosOrigStagingRoot"),
		VideosOrigRoot:        filepath.Join(tempDir, "VideosOrigRoot"),

		GooglePhotos: camflowconfig.GooglePhotosConfig{
			ClientId:     "test-client-id",
			ClientSecret: "test-client-secret",
			RedirectURI:  "test-redirect-uri",

			DefaultAlbums: defaultAlbums,

			// Does not set ToFav or KeywordAlbums fields.
		},
	}
	for _, dir := range []string{
		c.PhotosOrigRoot,
		c.PhotosExportStagingDir,
		c.PhotosExportDir,
		c.VideosOrigStagingRoot,
		c.VideosOrigRoot,
	} {
		require.NoError(t, os.MkdirAll(dir, 0755), dir)
	}
	return c
}
