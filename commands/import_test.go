package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/ccfrost/camflow/camflowconfig"
	"github.com/schollz/progressbar/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetFilesAndSize(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "camflow-test-*")
	require.NoError(t, err, "Failed to create temp directory")
	defer os.RemoveAll(tmpDir)

	// Create test files with known sizes
	files := map[string]int64{
		"test1.CR3":   100,
		"test2.JPG":   200,
		"test3.MP4":   300,
		"ignored.txt": 400,
	}

	for name, size := range files {
		path := filepath.Join(tmpDir, name)
		data := make([]byte, size)
		require.NoError(t, os.WriteFile(path, data, 0644))
	}

	// Create a subdirectory with more files
	subDirInclude := filepath.Join(tmpDir, "101CANON")
	require.NoError(t, os.MkdirAll(subDirInclude, 0755))
	subFiles := map[string]int64{
		"sub1.cr3": 150,
		"sub2.jpg": 250,
	}
	for name, size := range subFiles {
		path := filepath.Join(subDirInclude, name)
		data := make([]byte, size)
		require.NoError(t, os.WriteFile(path, data, 0644))
	}

	// Create a subdirectory with more files, but that should be excluded.
	subDirExclude := filepath.Join(tmpDir, "CANONMSC")
	require.NoError(t, os.MkdirAll(subDirExclude, 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(subDirExclude, "sub10.cr3"),
		make([]byte, 350),
		0644))

	gotFiles, gotSize, err := getFilesAndSize(tmpDir)
	require.NoError(t, err)

	// Calculate expected total size (only supported extensions)
	var expectedSize int64
	expectedCount := 0
	for name, size := range files {
		ext := filepath.Ext(name)
		if ext == ".CR3" || ext == ".JPG" || ext == ".MP4" {
			expectedSize += size
			expectedCount++
		}
	}
	for name, size := range subFiles {
		ext := filepath.Ext(name)
		if ext == ".cr3" || ext == ".jpg" {
			expectedSize += size
			expectedCount++
		}
	}

	assert.Equal(t, expectedSize, gotSize)
	assert.Equal(t, expectedCount, len(gotFiles), gotFiles)
}

func TestGetAvailableSpace(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "camflow-test-*")
	require.NoError(t, err, "Failed to create temp directory")
	defer os.RemoveAll(tmpDir)

	// Test with existing directory
	space, err := getAvailableSpace(tmpDir)
	require.NoError(t, err)
	assert.Greater(t, space, uint64(0), "Available space should be greater than 0")

	// Test with non-existent directory
	nonexistentDir := filepath.Join(tmpDir, "nonexistent")
	_, err = getAvailableSpace(nonexistentDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot stat directory")

	// Test with file instead of directory
	testFile := filepath.Join(tmpDir, "testfile")
	require.NoError(t, os.WriteFile(testFile, []byte("test"), 0644))
	space, err = getAvailableSpace(testFile)
	require.NoError(t, err)
	assert.Greater(t, space, uint64(0), "Available space should be greater than 0 for files too")
}

// createDummyFile creates dummy files for testing moveFiles.
func createDummyFile(t *testing.T, path string, content string, modTime time.Time) {
	t.Helper()
	err := os.MkdirAll(filepath.Dir(path), 0755)
	require.NoError(t, err, "Failed to create directory for dummy file: %s", filepath.Dir(path))
	err = os.WriteFile(path, []byte(content), 0644)
	require.NoError(t, err, "Failed to write dummy file: %s", path)
	err = os.Chtimes(path, modTime, modTime)
	require.NoError(t, err, "Failed to set mod time for dummy file: %s", path)
}

// setupMoveFilesTest sets up directories and config for moveFiles tests.
func setupMoveFilesTest(t *testing.T) (config camflowconfig.CamflowConfig, srcRoot, photosToProcessRoot, videosExportQueueRoot string, cleanup func()) {
	t.Helper()
	sdcardRoot := t.TempDir()
	mediaRoot := t.TempDir()

	srcRoot = filepath.Join(sdcardRoot, "DCIM")
	photosToProcessRoot = filepath.Join(mediaRoot, "photos-to-process")
	videosExportQueueRoot = filepath.Join(mediaRoot, "videos-export-queue")

	// Create the base source DCIM directory
	err := os.MkdirAll(srcRoot, 0755)
	require.NoError(t, err)
	// Create the base destination directories
	err = os.MkdirAll(photosToProcessRoot, 0755)
	require.NoError(t, err)
	err = os.MkdirAll(videosExportQueueRoot, 0755)
	require.NoError(t, err)

	config = camflowconfig.CamflowConfig{
		PhotosToProcessRoot:   photosToProcessRoot,
		VideosExportQueueRoot: videosExportQueueRoot,
		// Other config fields can be default/zero if not used by moveFiles directly
	}

	cleanup = func() {
		// os.RemoveAll(mediaRoot) // Handled by t.TempDir()
		// os.RemoveAll(sdcardRoot) // Handled by t.TempDir()
	}

	return config, srcRoot, photosToProcessRoot, videosExportQueueRoot, cleanup
}

// Helper struct for defining test file scenarios
type testFileCase struct {
	srcRelPath string // Relative path within the source DCIM dir (e.g., "100CANON/IMG_0001.JPG")
	content    string
	modTime    time.Time
	fileType   string // "photo", "video", "unsupported", "ignored"
}

// Helper function to calculate expected target path
func calculateExpectedTargetPath(tc testFileCase, photoDir, videoDir string) string {
	if tc.fileType != "photo" && tc.fileType != "video" {
		return "" // No target for unsupported/ignored
	}

	year, month, day := tc.modTime.Date()
	dateSubDir := fmt.Sprintf("%d/%02d/%02d", year, month, day)
	baseName := filepath.Base(tc.srcRelPath)
	targetBaseName := fmt.Sprintf("%d-%02d-%02d-%s", year, month, day, baseName)

	var targetDir string
	if tc.fileType == "photo" {
		targetDir = photoDir
	} else {
		targetDir = videoDir
	}
	return filepath.Join(targetDir, dateSubDir, targetBaseName)
}

func TestMoveFiles(t *testing.T) {
	bar := progressbar.DefaultBytesSilent(-1, "moving:")

	// --- Test Case: Success, keepSrc=false ---
	t.Run("SuccessKeepSrcFalse", func(t *testing.T) {
		config, srcDir, photoTargetRoot, videoTargetRoot, cleanup := setupMoveFilesTest(t)
		defer cleanup()

		// Define test file scenarios declaratively
		time1 := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
		time2 := time.Date(2024, 5, 2, 11, 0, 0, 0, time.UTC)
		time3 := time.Date(2024, 5, 3, 12, 0, 0, 0, time.UTC)

		testCases := []testFileCase{
			{srcRelPath: "100CANON/IMG_0001.JPG", content: "jpeg_content_1", modTime: time1, fileType: "photo"},
			{srcRelPath: "100CANON/IMG_0002.CR3", content: "raw_content_2", modTime: time2, fileType: "photo"},
			{srcRelPath: "101CANON/IMG_0004.JPG", content: "jpeg_content_4", modTime: time3, fileType: "photo"},
			{srcRelPath: "101CANON/VID_0003.MP4", content: "video_content_3", modTime: time1, fileType: "video"},
			{srcRelPath: "100CANON/NOTES.TXT", content: "unsupported", modTime: time1, fileType: "unsupported"},
			{srcRelPath: "MISC/OTHER.DAT", content: "misc_data", modTime: time1, fileType: "ignored"}, // Ignored by isDcimMediaDir
		}

		// Setup: Create source files
		srcPaths := make(map[string]string) // Store full source paths for later verification
		for _, tc := range testCases {
			fullSrcPath := filepath.Join(srcDir, tc.srcRelPath)
			srcPaths[tc.srcRelPath] = fullSrcPath
			createDummyFile(t, fullSrcPath, tc.content, tc.modTime)
		}

		// Run moveFiles
		result, err := moveFiles(config, srcDir, false, bar) // keepSrc = false
		require.NoError(t, err)

		// Verification: Check targets and source deletion
		expectedPhotoResultMap := make(map[string]int)
		expectedVideoResultMap := make(map[string]int)

		for _, tc := range testCases {
			fullSrcPath := srcPaths[tc.srcRelPath]
			expectedTarget := calculateExpectedTargetPath(tc, photoTargetRoot, videoTargetRoot)

			if tc.fileType == "photo" || tc.fileType == "video" {
				// Verify target file
				require.NotEmpty(t, expectedTarget, "Expected target path should not be empty for %s", tc.srcRelPath)
				content, err := os.ReadFile(expectedTarget)
				require.NoError(t, err, "Failed to read target file %s for source %s", expectedTarget, tc.srcRelPath)
				assert.Equal(t, tc.content, string(content), "Content mismatch for %s", tc.srcRelPath)
				info, err := os.Stat(expectedTarget)
				require.NoError(t, err, "Failed to stat target file %s for source %s", expectedTarget, tc.srcRelPath)
				// Use Truncate for potentially higher precision OS/filesystems
				assert.True(t, tc.modTime.Truncate(time.Second).Equal(info.ModTime().Truncate(time.Second)),
					"ModTime mismatch for %s: expected %v, got %v", tc.srcRelPath, tc.modTime, info.ModTime())

				// Verify source file deleted (since keepSrc=false)
				_, err = os.Stat(fullSrcPath)
				assert.True(t, os.IsNotExist(err), "Source file %s should be deleted", tc.srcRelPath)

				// Add to expected result map
				srcRelDir := filepath.Dir(fullSrcPath)
				if tc.fileType == "photo" {
					expectedPhotoResultMap[srcRelDir]++
				} else {
					expectedVideoResultMap[srcRelDir]++
				}

			} else { // unsupported or ignored
				// Verify target file does NOT exist
				if expectedTarget != "" { // Should be empty, but check just in case
					_, err = os.Stat(expectedTarget)
					assert.True(t, os.IsNotExist(err), "Target file %s should NOT exist for unsupported/ignored source %s", expectedTarget, tc.srcRelPath)
				}
				// Verify source file was NOT deleted
				_, err = os.Stat(fullSrcPath)
				assert.NoError(t, err, "Source file %s should NOT be deleted", tc.srcRelPath)
			}
		}

		// Convert expected result maps to slices for comparison
		expectedPhotoResult := []ImportDirEntry{}
		for dir, count := range expectedPhotoResultMap {
			expectedPhotoResult = append(expectedPhotoResult, ImportDirEntry{RelativeDir: dir, Count: count})
		}
		expectedVideoResult := []ImportDirEntry{}
		for dir, count := range expectedVideoResultMap {
			expectedVideoResult = append(expectedVideoResult, ImportDirEntry{RelativeDir: dir, Count: count})
		}

		// Sort slices for consistent comparison with ElementsMatch (optional but good practice)
		sort.Slice(expectedPhotoResult, func(i, j int) bool { return expectedPhotoResult[i].RelativeDir < expectedPhotoResult[j].RelativeDir })
		sort.Slice(expectedVideoResult, func(i, j int) bool { return expectedVideoResult[i].RelativeDir < expectedVideoResult[j].RelativeDir })
		sort.Slice(result.Photos, func(i, j int) bool { return result.Photos[i].RelativeDir < result.Photos[j].RelativeDir })
		sort.Slice(result.Videos, func(i, j int) bool { return result.Videos[i].RelativeDir < result.Videos[j].RelativeDir })

		assert.ElementsMatch(t, expectedPhotoResult, result.Photos, "Photo import results mismatch")
		assert.ElementsMatch(t, expectedVideoResult, result.Videos, "Video import results mismatch")
	})

	// --- Test Case: Success, keepSrc=true ---
	t.Run("SuccessKeepSrcTrue", func(t *testing.T) {
		config, srcDir, photoTargetRoot, videoTargetRoot, cleanup := setupMoveFilesTest(t)
		defer cleanup()

		// Define test file scenarios
		time1 := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
		time2 := time.Date(2024, 5, 2, 11, 0, 0, 0, time.UTC)

		testCases := []testFileCase{
			{srcRelPath: "100CANON/IMG_0001.JPG", content: "jpeg_content_keep", modTime: time1, fileType: "photo"},
			{srcRelPath: "101CANON/VID_0002.MP4", content: "video_content_keep", modTime: time2, fileType: "video"},
			{srcRelPath: "100CANON/NOTES.TXT", content: "unsupported_keep", modTime: time1, fileType: "unsupported"},
		}

		// Setup: Create source files
		srcPaths := make(map[string]string)
		for _, tc := range testCases {
			fullSrcPath := filepath.Join(srcDir, tc.srcRelPath)
			srcPaths[tc.srcRelPath] = fullSrcPath
			createDummyFile(t, fullSrcPath, tc.content, tc.modTime)
		}

		// Run moveFiles
		result, err := moveFiles(config, srcDir, true, bar) // keepSrc = true
		require.NoError(t, err)

		// Verification: Check targets and source *retention*
		expectedPhotoResultMap := make(map[string]int)
		expectedVideoResultMap := make(map[string]int)

		for _, tc := range testCases {
			fullSrcPath := srcPaths[tc.srcRelPath]
			expectedTarget := calculateExpectedTargetPath(tc, photoTargetRoot, videoTargetRoot)

			if tc.fileType == "photo" || tc.fileType == "video" {
				// Verify target file
				require.NotEmpty(t, expectedTarget, "Expected target path should not be empty for %s", tc.srcRelPath)
				content, err := os.ReadFile(expectedTarget)
				require.NoError(t, err, "Failed to read target file %s for source %s", expectedTarget, tc.srcRelPath)
				assert.Equal(t, tc.content, string(content), "Content mismatch for %s", tc.srcRelPath)
				info, err := os.Stat(expectedTarget)
				require.NoError(t, err, "Failed to stat target file %s for source %s", expectedTarget, tc.srcRelPath)
				assert.True(t, tc.modTime.Truncate(time.Second).Equal(info.ModTime().Truncate(time.Second)),
					"ModTime mismatch for %s: expected %v, got %v", tc.srcRelPath, tc.modTime, info.ModTime())

				// Verify source file was NOT deleted (since keepSrc=true)
				_, err = os.Stat(fullSrcPath)
				assert.NoError(t, err, "Source file %s should NOT be deleted when keepSrc=true", tc.srcRelPath)

				// Add to expected result map
				srcRelDir := filepath.Dir(fullSrcPath)
				if tc.fileType == "photo" {
					expectedPhotoResultMap[srcRelDir]++
				} else {
					expectedVideoResultMap[srcRelDir]++
				}

			} else { // unsupported or ignored
				// Verify target file does NOT exist
				if expectedTarget != "" {
					_, err = os.Stat(expectedTarget)
					assert.True(t, os.IsNotExist(err), "Target file %s should NOT exist for unsupported/ignored source %s", expectedTarget, tc.srcRelPath)
				}
				// Verify source file was NOT deleted
				_, err = os.Stat(fullSrcPath)
				assert.NoError(t, err, "Source file %s should NOT be deleted", tc.srcRelPath)
			}
		}

		// Convert expected result maps to slices
		expectedPhotoResult := []ImportDirEntry{}
		for dir, count := range expectedPhotoResultMap {
			expectedPhotoResult = append(expectedPhotoResult, ImportDirEntry{RelativeDir: dir, Count: count})
		}
		expectedVideoResult := []ImportDirEntry{}
		for dir, count := range expectedVideoResultMap {
			expectedVideoResult = append(expectedVideoResult, ImportDirEntry{RelativeDir: dir, Count: count})
		}

		// Sort slices for consistent comparison
		sort.Slice(expectedPhotoResult, func(i, j int) bool { return expectedPhotoResult[i].RelativeDir < expectedPhotoResult[j].RelativeDir })
		sort.Slice(expectedVideoResult, func(i, j int) bool { return expectedVideoResult[i].RelativeDir < expectedVideoResult[j].RelativeDir })
		sort.Slice(result.Photos, func(i, j int) bool { return result.Photos[i].RelativeDir < result.Photos[j].RelativeDir })
		sort.Slice(result.Videos, func(i, j int) bool { return result.Videos[i].RelativeDir < result.Videos[j].RelativeDir })

		assert.ElementsMatch(t, expectedPhotoResult, result.Photos, "Photo import results mismatch (keepSrc=true)")
		assert.ElementsMatch(t, expectedVideoResult, result.Videos, "Video import results mismatch (keepSrc=true)")
	})

	// --- Test Case: Empty Source Directory ---
	t.Run("EmptySourceDir", func(t *testing.T) {
		config, srcDir, _, _, cleanup := setupMoveFilesTest(t)
		defer cleanup()

		// Run moveFiles on an empty directory
		result, err := moveFiles(config, srcDir, false, bar)
		require.NoError(t, err)

		// Verify ImportResult is empty
		assert.Empty(t, result.Photos)
		assert.Empty(t, result.Videos)
	})

	// --- Test Case: Copy Error (Destination Not Writable) ---
	t.Run("ErrorCopyCannotWriteDest", func(t *testing.T) {
		config, srcDir, photoTargetRoot, _, cleanup := setupMoveFilesTest(t)
		defer cleanup()

		time1 := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
		srcPhoto1Path := filepath.Join(srcDir, "100CANON", "IMG_COPY_ERR.JPG")
		createDummyFile(t, srcPhoto1Path, "copy_error_content", time1)

		// Make the photo target dir root read-only BEFORE calling moveFiles
		err := os.Chmod(photoTargetRoot, 0555)
		require.NoError(t, err)
		// Attempt to restore permissions during cleanup, might fail if test fails early
		defer os.Chmod(photoTargetRoot, 0755)

		// Run moveFiles - expect failure during copyFile's MkdirAll or Create
		result, err := moveFiles(config, srcDir, false, bar)
		require.Error(t, err, "moveFiles should fail when destination is not writable")

		// Check the error message indicates a permission or creation issue
		assert.ErrorContains(t, err, "failed to create dir") // copyFile should fail here

		// Verify ImportResult is empty because the operation failed
		assert.Empty(t, result.Photos, "Photos result should be empty on error")
		assert.Empty(t, result.Videos, "Videos result should be empty on error")

		// Verify source file was NOT deleted because the copy failed
		_, err = os.Stat(srcPhoto1Path)
		assert.NoError(t, err, "Source file should NOT be deleted on copy error")
	})

	// Note: Testing os.Remove failure is complex to set up reliably across platforms
	// without modifying code or requiring special permissions. The current code correctly
	// returns the error from os.Remove if it occurs.
}

func TestIsDcimMediaDir(t *testing.T) {
	for _, tt := range []struct {
		name string
		dir  string
		want bool
	}{
		{"Valid 3-digit prefix", "100CANON", true},
		{"Valid different numbers", "999TEST", true},
		{"Too short", "12", false},
		{"Non-numeric prefix", "ABC123", false},
		{"Empty string", "", false},
		{"Mixed numeric prefix", "1A2TEST", false},
		{"All numeric", "123456", true},
		{"Canon misc", "CANONMSC", false},
	} {
		t.Run(tt.dir+": "+tt.name, func(t *testing.T) {
			got := isDcimMediaDir(tt.dir)
			assert.Equal(t, tt.want, got, "isDcimMediaDir(%q)", tt.dir)
		})
	}
}

func TestDeleteEmptyDirs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "camflow-test-*")
	require.NoError(t, err, "Failed to create temp directory")
	defer os.RemoveAll(tmpDir)

	// Create test directory structure
	dirs := []string{
		filepath.Join(tmpDir, "empty1"),
		filepath.Join(tmpDir, "empty2"),
		filepath.Join(tmpDir, "notempty1"),
		filepath.Join(tmpDir, "notempty2"),
	}

	for _, dir := range dirs {
		require.NoError(t, os.MkdirAll(dir, 0755))
	}

	// Create some files in the not-empty directories
	require.NoError(t, os.WriteFile(filepath.Join(dirs[2], "file1.txt"), []byte("content"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dirs[3], "file2.txt"), []byte("content"), 0644))

	// Create list of files that reference the empty directories
	files := []string{
		filepath.Join(dirs[0], "deleted1.jpg"),
		filepath.Join(dirs[1], "deleted2.jpg"),
	}

	require.NoError(t, deleteEmptyDirs(files))

	// Check that empty directories were removed
	_, err = os.Stat(dirs[0])
	assert.True(t, os.IsNotExist(err), "Expected empty directory to be removed: %s", dirs[0])
	_, err = os.Stat(dirs[1])
	assert.True(t, os.IsNotExist(err), "Expected empty directory to be removed: %s", dirs[1])

	// Check that non-empty directories still exist
	_, err = os.Stat(dirs[2])
	assert.NoError(t, err, "Expected non-empty directory to exist: %s", dirs[2])
	_, err = os.Stat(dirs[3])
	assert.NoError(t, err, "Expected non-empty directory to exist: %s", dirs[3])
}
