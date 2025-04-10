package commands

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMoveFilesAndFlatten(t *testing.T) {
	// Create temporary test directories
	tmpDir, err := os.MkdirTemp("", "camedia-test-*")
	require.NoError(t, err, "Failed to create temp directory")
	defer os.RemoveAll(tmpDir)

	srcDir := filepath.Join(tmpDir, "sdcard", "DCIM")
	photoDir := filepath.Join(tmpDir, "photos")
	videoDir := filepath.Join(tmpDir, "videos")

	// Create test directory structure
	dirs := []string{
		filepath.Join(srcDir, "100CANON"),
		filepath.Join(srcDir, "101CANON"),
	}
	for _, dir := range dirs {
		require.NoError(t, os.MkdirAll(dir, 0755), "Failed to create test directory: %s", dir)
	}

	// Create test files with known modification times
	// TODO: create file in MISC and make sure untouched.
	modTime := time.Date(2024, 4, 1, 12, 0, 0, 0, time.UTC)
	testFiles := []struct {
		path    string
		content []byte
		modTime time.Time
	}{
		{filepath.Join(dirs[0], "IMG_0001.CR3"), []byte("cr3 content"), modTime},
		{filepath.Join(dirs[0], "IMG_0001.JPG"), []byte("jpg content"), modTime},
		{filepath.Join(dirs[1], "IMG_0002.MP4"), []byte("mp4 content"), modTime.Add(time.Hour)},
		{filepath.Join(dirs[1], "README.txt"), []byte("ignored"), modTime},
	}

	for _, tf := range testFiles {
		require.NoError(t, os.WriteFile(tf.path, tf.content, 0644), "Failed to create test file: %s", tf.path)
		require.NoError(t, os.Chtimes(tf.path, tf.modTime, tf.modTime), "Failed to set modtime for: %s", tf.path)
	}

	// Create progress bar for testing
	bar := progressbar.NewOptions64(
		1000,
		progressbar.OptionSetDescription("testing"),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetWidth(40),
	)

	// Run the function being tested
	require.NoError(t, moveFilesAndFlatten(srcDir, photoDir, videoDir, false, bar))

	// Verify results
	tests := []struct {
		desc        string
		path        string
		shouldExist bool
		content     []byte
		modTime     time.Time
	}{
		{"CR3 moved correctly", filepath.Join(photoDir, "IMG_0001.CR3"), true, []byte("cr3 content"), modTime},
		{"JPG moved correctly", filepath.Join(photoDir, "IMG_0001.JPG"), true, []byte("jpg content"), modTime},
		{"MP4 moved correctly", filepath.Join(videoDir, "IMG_0002.MP4"), true, []byte("mp4 content"), modTime.Add(time.Hour)},
		{"Ignored file not moved", filepath.Join(photoDir, "README.txt"), false, nil, time.Time{}},
		{"Source CR3 removed", filepath.Join(dirs[0], "IMG_0001.CR3"), false, nil, time.Time{}},
		{"Source JPG removed", filepath.Join(dirs[0], "IMG_0001.JPG"), false, nil, time.Time{}},
		{"Source MP4 removed", filepath.Join(dirs[1], "IMG_0002.MP4"), false, nil, time.Time{}},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			info, err := os.Stat(tt.path)
			if tt.shouldExist {
				require.NoError(t, err, "Expected %s to exist", tt.path)

				// Check content
				got, err := os.ReadFile(tt.path)
				require.NoError(t, err, "Failed to read file: %s", tt.path)
				assert.Equal(t, string(tt.content), string(got), "Content mismatch for %s", tt.path)

				// Check modification time
				assert.True(t, info.ModTime().Equal(tt.modTime), "Modtime mismatch for %s: got %s, want %s",
					tt.path, info.ModTime(), tt.modTime)
			} else {
				assert.True(t, os.IsNotExist(err), "Expected %s to not exist, but it does", tt.path)
			}
		})
	}

	// TODO: check that CANON101 is deleted
}

func TestMoveFilesAndFlattenErrors(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "camedia-test-*")
	require.NoError(t, err, "Failed to create temp directory")
	defer os.RemoveAll(tmpDir)

	srcDir := filepath.Join(tmpDir, "DCIM")
	require.NoError(t, os.MkdirAll(srcDir, 0755), "Failed to create source directory")

	readOnlyDir := filepath.Join(tmpDir, "readonly")
	require.NoError(t, os.MkdirAll(readOnlyDir, 0444), "Failed to create readonly directory")

	bar := progressbar.NewOptions64(1000)

	tests := []struct {
		desc     string
		srcDir   string
		photoDir string
		videoDir string
		wantErr  bool
	}{
		{
			desc:     "Source directory doesn't exist",
			srcDir:   filepath.Join(tmpDir, "nonexistent"),
			photoDir: filepath.Join(tmpDir, "photos"),
			videoDir: filepath.Join(tmpDir, "videos"),
			wantErr:  true,
		},
		{
			desc:     "Can't create photo directory",
			srcDir:   srcDir,
			photoDir: filepath.Join(readOnlyDir, "photos"),
			videoDir: filepath.Join(tmpDir, "videos"),
			wantErr:  true,
		},
		{
			desc:     "Can't create video directory",
			srcDir:   srcDir,
			photoDir: filepath.Join(tmpDir, "photos"),
			videoDir: filepath.Join(readOnlyDir, "videos"),
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			err := moveFilesAndFlatten(tt.srcDir, tt.photoDir, tt.videoDir, false, bar)
			if tt.wantErr {
				assert.Error(t, err, "Expected an error but got none")
			} else {
				assert.NoError(t, err, "Got unexpected error")
			}
		})
	}
}
