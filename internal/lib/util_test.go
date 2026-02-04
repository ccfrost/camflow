package lib

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ccfrost/camflow/internal/config"
	"github.com/schollz/progressbar/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestConfig(t *testing.T, photosDefaultAlbum, videosDefaultAlbum string) config.CamflowConfig {
	t.Helper()

	tempDir := t.TempDir()
	c := config.CamflowConfig{
		PhotosProcessQueueRoot: filepath.Join(tempDir, "PhotosProcessQueueRoot"),
		PhotosUploadQueueDir:   filepath.Join(tempDir, "PhotosUploadQueueDir"),
		PhotosUploadedRoot:     filepath.Join(tempDir, "PhotosUploadedRoot"),

		VideosUploadQueueRoot: filepath.Join(tempDir, "VideosUploadQueueRoot"),
		VideosUploadedRoot:    filepath.Join(tempDir, "VideosUploadedRoot"),

		GooglePhotos: config.GooglePhotosConfig{
			ClientId:     "test-client-id",
			ClientSecret: "test-client-secret",
			RedirectURI:  "test-redirect-uri",

			Photos: config.GPPhotosConfig{
				DefaultAlbum: photosDefaultAlbum,
			},
			Videos: config.GPVideosConfig{
				DefaultAlbum: videosDefaultAlbum,
			},

			// Does not set ToFav or KeywordAlbums fields.
		},
	}
	c.LocalPhotos = config.LocalPhotosConfig{
		ProcessQueueRoot: c.PhotosProcessQueueRoot,
		UploadQueueDir:   c.PhotosUploadQueueDir,
		UploadedRoot:     c.PhotosUploadedRoot,
	}
	c.LocalVideos = config.LocalVideosConfig{
		UploadQueueRoot: c.VideosUploadQueueRoot,
		UploadedRoot:    c.VideosUploadedRoot,
	}
	for _, dir := range []string{
		c.PhotosProcessQueueRoot,
		c.PhotosUploadQueueDir,
		c.PhotosUploadedRoot,
		c.VideosUploadQueueRoot,
		c.VideosUploadedRoot,
	} {
		require.NoError(t, os.MkdirAll(dir, 0755), dir)
	}
	return c
}

func TestCopyFile(t *testing.T) {
	// --- Setup ---
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	bar := progressbar.DefaultBytesSilent(-1, "copying:") // Use a silent bar for testing

	// --- Success Case ---
	t.Run("Success", func(t *testing.T) {
		srcFile := filepath.Join(srcDir, "source_success.txt")
		dstFile := filepath.Join(dstDir, "subdir", "dest_success.txt") // Test subdirectory creation
		content := []byte("test content for copyfile")
		modTime := time.Date(2023, 10, 27, 10, 0, 0, 0, time.UTC)
		size := int64(len(content))

		// Create source file
		err := os.WriteFile(srcFile, content, 0644)
		require.NoError(t, err, "Failed to create source file")

		// Perform the copy
		err = copyFile(srcFile, dstFile, size, modTime, bar)
		require.NoError(t, err, "copyFile failed unexpectedly")

		// Verify destination file exists
		info, err := os.Stat(dstFile)
		require.NoError(t, err, "Destination file does not exist after copy")
		assert.False(t, info.IsDir(), "Destination should be a file")

		// Verify destination file content
		dstContent, err := os.ReadFile(dstFile)
		require.NoError(t, err, "Failed to read destination file")
		assert.Equal(t, content, dstContent, "Destination file content mismatch")

		// Verify destination modification time
		// Use Truncate for potentially higher precision OS/filesystems
		assert.True(t, modTime.Truncate(time.Second).Equal(info.ModTime().Truncate(time.Second)),
			"Modification time mismatch: expected %v, got %v", modTime, info.ModTime())

		// Verify temp file is gone
		_, err = os.Stat(dstFile + ".tmp")
		assert.True(t, os.IsNotExist(err), "Temporary file should not exist after successful copy")
	})

	// --- Success Case (Zero Byte File) ---
	t.Run("SuccessZeroByte", func(t *testing.T) {
		srcFile := filepath.Join(srcDir, "source_zero.txt")
		dstFile := filepath.Join(dstDir, "subdir", "dest_zero.txt")
		content := []byte{} // Empty content
		modTime := time.Date(2023, 10, 27, 11, 0, 0, 0, time.UTC)
		size := int64(0)

		// Create source file
		err := os.WriteFile(srcFile, content, 0644)
		require.NoError(t, err, "Failed to create zero-byte source file")

		// Perform the copy
		err = copyFile(srcFile, dstFile, size, modTime, bar)
		require.NoError(t, err, "copyFile failed for zero-byte file")

		// Verify destination file exists and is zero size
		info, err := os.Stat(dstFile)
		require.NoError(t, err, "Zero-byte destination file does not exist")
		assert.Equal(t, int64(0), info.Size(), "Destination file should be zero bytes")

		// Verify destination modification time
		assert.True(t, modTime.Truncate(time.Second).Equal(info.ModTime().Truncate(time.Second)),
			"Modification time mismatch for zero-byte file: expected %v, got %v", modTime, info.ModTime())
	})

	// --- Error Case: Cannot Create Destination Directory ---
	t.Run("ErrorCannotCreateDestDir", func(t *testing.T) {
		// Create a read-only directory to prevent subdirectory creation
		readOnlyBaseDir := t.TempDir()
		err := os.Chmod(readOnlyBaseDir, 0555) // Read/execute only
		require.NoError(t, err, "Failed to make base directory read-only")
		// On some systems/CI, chmod might not prevent creation by root/owner,
		// but it's the standard way to attempt this for a test.

		srcFile := filepath.Join(srcDir, "source_err_dest.txt")
		dstFile := filepath.Join(readOnlyBaseDir, "forbidden_subdir", "dest_err.txt")
		content := []byte("should not be copied")
		modTime := time.Now()
		size := int64(len(content))

		// Create source file
		err = os.WriteFile(srcFile, content, 0644)
		require.NoError(t, err, "Failed to create source file for error test")

		// Perform the copy - expect failure
		err = copyFile(srcFile, dstFile, size, modTime, bar)
		require.Error(t, err, "copyFile should have failed when destination directory cannot be created")

		// Check if the error indicates a directory creation problem (optional, depends on exact error wrapping)
		assert.ErrorContains(t, err, "failed to create dir")

		// Verify destination file does not exist
		_, err = os.Stat(dstFile)
		assert.True(t, os.IsNotExist(err), "Destination file should not exist after failed copy")

		// Verify temp file does not exist
		_, err = os.Stat(dstFile + ".tmp")
		assert.True(t, os.IsNotExist(err), "Temporary file should not exist after failed copy")

		// Restore permissions for cleanup
		os.Chmod(readOnlyBaseDir, 0755)
	})

	// --- Error Case: Source Does Not Exist ---
	// (Lower priority per user feedback, but good to have)
	t.Run("ErrorSourceNotExist", func(t *testing.T) {
		srcFile := filepath.Join(srcDir, "nonexistent_source.txt")
		dstFile := filepath.Join(dstDir, "dest_src_err.txt")
		modTime := time.Now()
		size := int64(100) // Size doesn't matter much here

		err := copyFile(srcFile, dstFile, size, modTime, bar)
		require.Error(t, err, "copyFile should fail if source doesn't exist")
		assert.True(t, os.IsNotExist(err), "Error should be os.IsNotExist for missing source")

		// Verify destination file does not exist
		_, err = os.Stat(dstFile)
		assert.True(t, os.IsNotExist(err), "Destination file should not exist when source is missing")
	})
}
