package commands

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/ccfrost/camedia/camediaconfig"
	"github.com/schollz/progressbar/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetFilesAndSize(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "camedia-test-*")
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
	tmpDir, err := os.MkdirTemp("", "camedia-test-*")
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
func setupMoveFilesTest(t *testing.T) (config camediaconfig.CamediaConfig, srcRoot, photoStaging, videoStaging string, cleanup func()) {
	t.Helper()
	mediaRoot := t.TempDir()
	sdcardRoot := t.TempDir()

	srcRoot = filepath.Join(sdcardRoot, "DCIM")
	photoStaging = filepath.Join(mediaRoot, "photos-staging")
	videoStaging = filepath.Join(mediaRoot, "videos-staging")

	// Create the base source DCIM directory
	err := os.MkdirAll(srcRoot, 0755)
	require.NoError(t, err)
	// Create the base destination staging directories
	err = os.MkdirAll(photoStaging, 0755)
	require.NoError(t, err)
	err = os.MkdirAll(videoStaging, 0755)
	require.NoError(t, err)

	config = camediaconfig.CamediaConfig{
		MediaRoot: mediaRoot,
		// Other config fields can be default/zero if not used by moveFiles directly
	}

	cleanup = func() {
		// os.RemoveAll(mediaRoot) // Handled by t.TempDir()
		// os.RemoveAll(sdcardRoot) // Handled by t.TempDir()
	}

	return config, srcRoot, photoStaging, videoStaging, cleanup
}

func TestMoveFiles(t *testing.T) {
	bar := progressbar.DefaultBytesSilent(-1, "moving:")

	// --- Test Case: Success, keepSrc=false ---
	t.Run("SuccessKeepSrcFalse", func(t *testing.T) {
		config, srcDir, photoStaging, videoStaging, cleanup := setupMoveFilesTest(t)
		defer cleanup()

		// Files with different dates and types
		time1 := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
		time2 := time.Date(2024, 5, 2, 11, 0, 0, 0, time.UTC)
		time3 := time.Date(2024, 5, 3, 12, 0, 0, 0, time.UTC)

		srcPhoto1Path := filepath.Join(srcDir, "100CANON", "IMG_0001.JPG")
		srcPhoto2Path := filepath.Join(srcDir, "100CANON", "IMG_0002.CR3")
		srcPhoto3Path := filepath.Join(srcDir, "101CANON", "IMG_0004.JPG")
		srcVideo1Path := filepath.Join(srcDir, "101CANON", "VID_0003.MP4")
		srcUnsupportedPath := filepath.Join(srcDir, "100CANON", "NOTES.TXT")
		srcMiscDirPath := filepath.Join(srcDir, "MISC", "OTHER.DAT") // Should be ignored by isDcimMediaDir

		createDummyFile(t, srcPhoto1Path, "jpeg_content_1", time1)
		createDummyFile(t, srcPhoto2Path, "raw_content_2", time2)
		createDummyFile(t, srcPhoto3Path, "jpeg_content_4", time3)
		createDummyFile(t, srcVideo1Path, "video_content_3", time1)
		createDummyFile(t, srcUnsupportedPath, "unsupported", time1)
		createDummyFile(t, srcMiscDirPath, "misc_data", time1)

		// Expected target paths
		expectedPhoto1Target := filepath.Join(photoStaging, "2024/05/01", "2024-05-01-IMG_0001.JPG")
		expectedPhoto2Target := filepath.Join(photoStaging, "2024/05/02", "2024-05-02-IMG_0002.CR3")
		expectedPhoto3Target := filepath.Join(photoStaging, "2024/05/03", "2024-05-03-IMG_0004.JPG")
		expectedVideo1Target := filepath.Join(videoStaging, "2024/05/01", "2024-05-01-VID_0003.MP4")

		// Run moveFiles
		result, err := moveFiles(config, srcDir, false, bar) // keepSrc = false
		require.NoError(t, err)

		// Verify target files exist and content/modtime
		content, err := os.ReadFile(expectedPhoto1Target)
		require.NoError(t, err)
		assert.Equal(t, "jpeg_content_1", string(content))
		info, err := os.Stat(expectedPhoto1Target)
		require.NoError(t, err)
		assert.True(t, time1.Equal(info.ModTime()))

		content, err = os.ReadFile(expectedPhoto2Target)
		require.NoError(t, err)
		assert.Equal(t, "raw_content_2", string(content))
		info, err = os.Stat(expectedPhoto2Target)
		require.NoError(t, err)
		assert.True(t, time2.Equal(info.ModTime()))

		// Verify third photo target
		content, err = os.ReadFile(expectedPhoto3Target)
		require.NoError(t, err)
		assert.Equal(t, "jpeg_content_4", string(content))
		info, err = os.Stat(expectedPhoto3Target)
		require.NoError(t, err)
		assert.True(t, time3.Equal(info.ModTime()))

		content, err = os.ReadFile(expectedVideo1Target)
		require.NoError(t, err)
		assert.Equal(t, "video_content_3", string(content))
		info, err = os.Stat(expectedVideo1Target)
		require.NoError(t, err)
		assert.True(t, time1.Equal(info.ModTime()))

		// Verify source files were deleted
		_, err = os.Stat(srcPhoto1Path)
		assert.True(t, os.IsNotExist(err), "Source photo 1 should be deleted")
		_, err = os.Stat(srcPhoto2Path)
		assert.True(t, os.IsNotExist(err), "Source photo 2 should be deleted")
		_, err = os.Stat(srcPhoto3Path) // Verify third photo source deleted
		assert.True(t, os.IsNotExist(err), "Source photo 3 should be deleted")
		_, err = os.Stat(srcVideo1Path)
		assert.True(t, os.IsNotExist(err), "Source video 1 should be deleted")

		// Verify unsupported/ignored files were NOT moved and NOT deleted
		_, err = os.Stat(srcUnsupportedPath)
		assert.NoError(t, err, "Unsupported source file should NOT be deleted")
		_, err = os.Stat(filepath.Join(photoStaging, "2024/05/01", "2024-05-01-NOTES.TXT"))
		assert.True(t, os.IsNotExist(err), "Unsupported file should NOT be moved")

		_, err = os.Stat(srcMiscDirPath)
		assert.NoError(t, err, "MISC dir source file should NOT be deleted")
		_, err = os.Stat(filepath.Join(photoStaging, "2024/05/01", "2024-05-01-OTHER.DAT"))
		assert.True(t, os.IsNotExist(err), "MISC dir file should NOT be moved")

		// Verify ImportResult (Note: RelativeDir is the SOURCE directory)
		// Now expect two entries for photos from different source dirs
		expectedPhotoResult := []ImportDirEntry{
			{RelativeDir: filepath.Dir(srcPhoto1Path), Count: 2}, // IMG_0001.JPG, IMG_0002.CR3 from 100CANON
			{RelativeDir: filepath.Dir(srcPhoto3Path), Count: 1}, // IMG_0004.JPG from 101CANON
		}
		expectedVideoResult := []ImportDirEntry{
			{RelativeDir: filepath.Dir(srcVideo1Path), Count: 1}, // VID_0003.MP4 from 101CANON
		}
		// Sort expected results for comparison as ElementsMatch doesn't care about order, but makes debugging easier
		sort.Slice(expectedPhotoResult, func(i, j int) bool { return expectedPhotoResult[i].RelativeDir < expectedPhotoResult[j].RelativeDir })

		assert.ElementsMatch(t, expectedPhotoResult, result.Photos)
		assert.ElementsMatch(t, expectedVideoResult, result.Videos)
	})

	// --- Test Case: Success, keepSrc=true ---
	t.Run("SuccessKeepSrcTrue", func(t *testing.T) {
		config, srcDir, photoStaging, _, cleanup := setupMoveFilesTest(t)
		defer cleanup()

		time1 := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
		srcPhoto1Path := filepath.Join(srcDir, "100CANON", "IMG_0001.JPG")
		createDummyFile(t, srcPhoto1Path, "jpeg_content_keep", time1)
		expectedPhoto1Target := filepath.Join(photoStaging, "2024/05/01", "2024-05-01-IMG_0001.JPG")

		// Run moveFiles
		result, err := moveFiles(config, srcDir, true, bar) // keepSrc = true
		require.NoError(t, err)

		// Verify target file exists
		_, err = os.Stat(expectedPhoto1Target)
		require.NoError(t, err)

		// Verify source file was NOT deleted
		_, err = os.Stat(srcPhoto1Path)
		assert.NoError(t, err, "Source photo 1 should NOT be deleted when keepSrc=true")

		// Verify ImportResult
		expectedPhotoResult := []ImportDirEntry{{RelativeDir: filepath.Dir(srcPhoto1Path), Count: 1}}
		assert.ElementsMatch(t, expectedPhotoResult, result.Photos)
		assert.Empty(t, result.Videos)
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
		config, srcDir, photoStaging, _, cleanup := setupMoveFilesTest(t)
		defer cleanup()

		time1 := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
		srcPhoto1Path := filepath.Join(srcDir, "100CANON", "IMG_COPY_ERR.JPG")
		createDummyFile(t, srcPhoto1Path, "copy_error_content", time1)

		// Make the photo staging root read-only BEFORE calling moveFiles
		err := os.Chmod(photoStaging, 0555)
		require.NoError(t, err)
		// Attempt to restore permissions during cleanup, might fail if test fails early
		defer os.Chmod(photoStaging, 0755)

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
	tmpDir, err := os.MkdirTemp("", "camedia-test-*")
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
