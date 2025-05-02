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
