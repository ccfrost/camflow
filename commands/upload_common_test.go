package commands

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDatePrefix(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectYear  string
		expectMonth string
		expectDay   string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Valid date with suffix",
			input:       "2024-12-25-Christmas-Video.mp4",
			expectYear:  "2024",
			expectMonth: "12",
			expectDay:   "25",
			expectError: false,
		},
		{
			name:        "Valid date minimal suffix",
			input:       "2023-01-01-file.jpg",
			expectYear:  "2023",
			expectMonth: "01",
			expectDay:   "01",
			expectError: false,
		},
		{
			name:        "Valid date with long suffix",
			input:       "2025-06-20-very-long-filename-with-many-parts.mov",
			expectYear:  "2025",
			expectMonth: "06",
			expectDay:   "20",
			expectError: false,
		},
		{
			name:        "Valid date exactly four parts",
			input:       "2024-12-31-file",
			expectYear:  "2024",
			expectMonth: "12",
			expectDay:   "31",
			expectError: false,
		},
		{
			name:        "Invalid - year too short",
			input:       "24-12-25-file.mp4",
			expectError: true,
			errorMsg:    "invalid format: year '24' must be 4 characters long",
		},
		{
			name:        "Invalid - year too long",
			input:       "20244-12-25-file.mp4",
			expectError: true,
			errorMsg:    "invalid format: year '20244' must be 4 characters long",
		},
		{
			name:        "Invalid - month too short",
			input:       "2024-1-25-file.mp4",
			expectError: true,
			errorMsg:    "invalid format: month '1' must be 2 characters long",
		},
		{
			name:        "Invalid - month too long",
			input:       "2024-123-25-file.mp4",
			expectError: true,
			errorMsg:    "invalid format: month '123' must be 2 characters long",
		},
		{
			name:        "Invalid - day too short",
			input:       "2024-12-5-file.mp4",
			expectError: true,
			errorMsg:    "invalid format: day '5' must be 2 characters long",
		},
		{
			name:        "Invalid - day too long",
			input:       "2024-12-255-file.mp4",
			expectError: true,
			errorMsg:    "invalid format: day '255' must be 2 characters long",
		},
		{
			name:        "Invalid - only three parts",
			input:       "2024-12-31",
			expectError: true,
			errorMsg:    "invalid format: expected at least 4 parts separated by '-'",
		},
		{
			name:        "Invalid - only two parts",
			input:       "2024-12",
			expectError: true,
			errorMsg:    "invalid format: expected at least 4 parts separated by '-'",
		},
		{
			name:        "Invalid - only one part",
			input:       "2024",
			expectError: true,
			errorMsg:    "invalid format: expected at least 4 parts separated by '-'",
		},
		{
			name:        "Invalid - no parts",
			input:       "",
			expectError: true,
			errorMsg:    "invalid format: expected at least 4 parts separated by '-'",
		},
		{
			name:        "Invalid - no dashes",
			input:       "20241225file.mp4",
			expectError: true,
			errorMsg:    "invalid format: expected at least 4 parts separated by '-'",
		},
		{
			name:        "Invalid - empty month",
			input:       "2024--25-file.mp4",
			expectError: true,
			errorMsg:    "invalid format: month '' must be 2 characters long",
		},
		{
			name:        "Invalid - empty year (leading dash)",
			input:       "-2024-12-25-file.mp4",
			expectError: true,
			errorMsg:    "invalid format: year '' must be 4 characters long",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			year, month, day, err := parseDatePrefix(tt.input)

			if tt.expectError {
				require.Error(t, err, "Expected an error for input: %s", tt.input)
				assert.Contains(t, err.Error(), tt.errorMsg, "Error message should contain expected text")
			} else {
				require.NoError(t, err, "Expected no error for input: %s", tt.input)
				assert.Equal(t, tt.expectYear, year, "Year should match")
				assert.Equal(t, tt.expectMonth, month, "Month should match")
				assert.Equal(t, tt.expectDay, day, "Day should match")
			}
		})
	}
}

func TestParseDatePrefix_RealWorldExamples(t *testing.T) {
	// Test with actual filename patterns that might be encountered
	realWorldCases := []struct {
		filename    string
		expectYear  string
		expectMonth string
		expectDay   string
	}{
		{"2024-01-28-camflow-test-IMG_4286-1.MP4", "2024", "01", "28"},
		{"2025-06-20-vacation-video.mov", "2025", "06", "20"},
		{"2023-12-31-new-years-eve-party.mp4", "2023", "12", "31"},
		{"2024-02-29-leap-year-video.avi", "2024", "02", "29"},
		{"2022-07-04-independence-day.mp4", "2022", "07", "04"},
	}

	for _, tc := range realWorldCases {
		t.Run(tc.filename, func(t *testing.T) {
			year, month, day, err := parseDatePrefix(tc.filename)
			require.NoError(t, err)
			assert.Equal(t, tc.expectYear, year)
			assert.Equal(t, tc.expectMonth, month)
			assert.Equal(t, tc.expectDay, day)
		})
	}
}

func TestParseDatePrefix_InvalidRealWorldExamples(t *testing.T) {
	// Test with real-world filename patterns that should fail validation
	invalidCases := []struct {
		filename string
		errorMsg string
	}{
		{"2022-7-4-independence-day.mp4", "invalid format: month '7' must be 2 characters long"},
		{"2023-1-15-new-year.mp4", "invalid format: month '1' must be 2 characters long"},
		{"2024-12-5-christmas.mp4", "invalid format: day '5' must be 2 characters long"},
		{"22-12-25-short-year.mp4", "invalid format: year '22' must be 4 characters long"},
		{"2024-123-01-long-month.mp4", "invalid format: month '123' must be 2 characters long"},
	}

	for _, tc := range invalidCases {
		t.Run(tc.filename, func(t *testing.T) {
			_, _, _, err := parseDatePrefix(tc.filename)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errorMsg)
		})
	}
}

func TestFindExistingParent(t *testing.T) {
	// --- Test Case: Path exists directly ---
	t.Run("PathExistsDirectly", func(t *testing.T) {
		tempDir := t.TempDir()

		result, err := findExistingParent(tempDir)
		require.NoError(t, err)
		assert.Equal(t, tempDir, result)
	})

	// --- Test Case: Parent directory exists ---
	t.Run("ParentExists", func(t *testing.T) {
		tempDir := t.TempDir()
		nonExistentPath := filepath.Join(tempDir, "nonexistent", "subdir", "file.txt")

		result, err := findExistingParent(nonExistentPath)
		require.NoError(t, err)
		assert.Equal(t, tempDir, result)
	})

	// --- Test Case: Grandparent exists ---
	t.Run("GrandparentExists", func(t *testing.T) {
		tempDir := t.TempDir()
		subDir := filepath.Join(tempDir, "existing")
		require.NoError(t, os.Mkdir(subDir, 0755))

		nonExistentPath := filepath.Join(subDir, "nonexistent", "deeper", "file.txt")

		result, err := findExistingParent(nonExistentPath)
		require.NoError(t, err)
		assert.Equal(t, subDir, result)
	})
	// --- Test Case: Relative path ---
	t.Run("RelativePath", func(t *testing.T) {
		tempDir := t.TempDir()
		// Change to temp dir to test relative paths
		originalWd, err := os.Getwd()
		require.NoError(t, err)
		defer os.Chdir(originalWd)

		require.NoError(t, os.Chdir(tempDir))

		result, err := findExistingParent("nonexistent/file.txt")
		require.NoError(t, err)

		// Resolve symlinks for both to handle /var -> /private/var on macOS
		expectedReal, err := filepath.EvalSymlinks(tempDir)
		require.NoError(t, err)
		resultReal, err := filepath.EvalSymlinks(result)
		require.NoError(t, err)

		assert.Equal(t, expectedReal, resultReal)
	})

	// --- Test Case: Root filesystem (edge case) ---
	t.Run("ReachesRoot", func(t *testing.T) {
		// Use a path that definitely doesn't exist and should reach root
		nonExistentPath := "/definitely/does/not/exist/very/deep/path"

		result, err := findExistingParent(nonExistentPath)
		require.NoError(t, err)
		// Should return "/" (root) on Unix systems
		assert.Equal(t, "/", result)
	})
}

func TestIsSameFilesystem(t *testing.T) {
	// --- Test Case: Same directory ---
	t.Run("SameDirectory", func(t *testing.T) {
		tempDir := t.TempDir()
		path1 := filepath.Join(tempDir, "file1.txt")
		path2 := filepath.Join(tempDir, "file2.txt")

		same, err := isSameFilesystem(path1, path2)
		require.NoError(t, err)
		assert.True(t, same, "Files in same directory should be on same filesystem")
	})

	// --- Test Case: Same filesystem, different subdirectories ---
	t.Run("SameFilesystemDifferentDirs", func(t *testing.T) {
		tempDir := t.TempDir()

		// Create two subdirectories
		subDir1 := filepath.Join(tempDir, "subdir1")
		subDir2 := filepath.Join(tempDir, "subdir2")
		require.NoError(t, os.Mkdir(subDir1, 0755))
		require.NoError(t, os.Mkdir(subDir2, 0755))

		path1 := filepath.Join(subDir1, "file1.txt")
		path2 := filepath.Join(subDir2, "file2.txt")

		same, err := isSameFilesystem(path1, path2)
		require.NoError(t, err)
		assert.True(t, same, "Files in subdirectories of same parent should be on same filesystem")
	})

	// --- Test Case: Nonexistent paths on same filesystem ---
	t.Run("NonexistentPathsSameFS", func(t *testing.T) {
		tempDir := t.TempDir()

		path1 := filepath.Join(tempDir, "deep", "nonexistent", "path1", "file.txt")
		path2 := filepath.Join(tempDir, "another", "deep", "path2", "file.txt")

		same, err := isSameFilesystem(path1, path2)
		require.NoError(t, err)
		assert.True(t, same, "Nonexistent paths under same parent should be on same filesystem")
	})

	// --- Test Case: Mixed existing and nonexistent paths ---
	t.Run("MixedExistingNonexistent", func(t *testing.T) {
		tempDir := t.TempDir()

		// Create one subdirectory
		subDir := filepath.Join(tempDir, "existing")
		require.NoError(t, os.Mkdir(subDir, 0755))

		existingPath := filepath.Join(subDir, "file1.txt")
		nonexistentPath := filepath.Join(tempDir, "nonexistent", "deep", "file2.txt")

		same, err := isSameFilesystem(existingPath, nonexistentPath)
		require.NoError(t, err)
		assert.True(t, same, "Existing and nonexistent paths under same parent should be on same filesystem")
	})

	// --- Test Case: Root filesystem comparison ---
	t.Run("RootFilesystem", func(t *testing.T) {
		// Compare two paths that should both resolve to root
		path1 := "/definitely/does/not/exist/path1"
		path2 := "/definitely/does/not/exist/path2"

		same, err := isSameFilesystem(path1, path2)
		require.NoError(t, err)
		assert.True(t, same, "Paths that resolve to root should be on same filesystem")
	})

	// --- Test Case: Symlinks (if they exist) ---
	t.Run("SymlinksToSameTarget", func(t *testing.T) {
		tempDir := t.TempDir()

		// Create a target directory
		targetDir := filepath.Join(tempDir, "target")
		require.NoError(t, os.Mkdir(targetDir, 0755))

		// Create symlinks to the same target
		symlink1 := filepath.Join(tempDir, "symlink1")
		symlink2 := filepath.Join(tempDir, "symlink2")

		// Only create symlinks if the system supports them
		if err := os.Symlink(targetDir, symlink1); err != nil {
			t.Skip("System doesn't support symlinks, skipping test")
		}
		require.NoError(t, os.Symlink(targetDir, symlink2))

		path1 := filepath.Join(symlink1, "file1.txt")
		path2 := filepath.Join(symlink2, "file2.txt")

		same, err := isSameFilesystem(path1, path2)
		require.NoError(t, err)
		assert.True(t, same, "Paths through symlinks to same target should be on same filesystem")
	})

	// --- Test Case: Error handling for invalid paths ---
	t.Run("InvalidPath", func(t *testing.T) {
		// Test with a path that contains null bytes (invalid on most filesystems)
		invalidPath := "/invalid\x00path"
		validPath := "/tmp"

		_, err := isSameFilesystem(invalidPath, validPath)
		assert.Error(t, err, "Should error on invalid path characters")
	})
}

func TestIsSameFilesystemIntegration(t *testing.T) {
	// --- Integration test mimicking the upload scenario ---
	t.Run("UploadScenario", func(t *testing.T) {
		tempDir := t.TempDir()

		// Create directory structure similar to the upload scenario
		exportQueueRoot := filepath.Join(tempDir, "export-queue")
		exportedRoot := filepath.Join(tempDir, "exported")
		require.NoError(t, os.Mkdir(exportQueueRoot, 0755))
		require.NoError(t, os.Mkdir(exportedRoot, 0755))

		// Test paths that would be used in uploadMediaItem
		srcFile := filepath.Join(exportQueueRoot, "2024", "06", "20", "video.mp4")
		destFile := filepath.Join(exportedRoot, "2024", "06", "20", "video.mp4")

		same, err := isSameFilesystem(srcFile, destFile)
		require.NoError(t, err)
		assert.True(t, same, "Export queue and exported directories should be on same filesystem in test")

		// Verify the function found the correct existing parents
		srcParent, err := findExistingParent(srcFile)
		require.NoError(t, err)
		assert.Equal(t, exportQueueRoot, srcParent)

		destParent, err := findExistingParent(destFile)
		require.NoError(t, err)
		assert.Equal(t, exportedRoot, destParent)
	})
}

// TestIsSameFilesystem_DifferentFilesystems tests the function with paths on different filesystems
// by detecting commonly different mount points on the current system.
func TestIsSameFilesystem_DifferentFilesystems(t *testing.T) {
	var path1, path2 string

	switch runtime.GOOS {
	case "darwin":
		// macOS: Try common different filesystem combinations
		candidates := [][]string{
			{"/tmp", "/System"},      // /tmp vs /System (usually different)
			{"/tmp", "/private/var"}, // /tmp vs /private/var
			{"/", "/private/tmp"},    // root vs /private/tmp
		}

		// Check for mounted volumes
		if volumes, err := os.ReadDir("/Volumes"); err == nil && len(volumes) > 0 {
			for _, vol := range volumes {
				volPath := filepath.Join("/Volumes", vol.Name())
				// Skip .localized and other system files
				if !vol.IsDir() || vol.Name()[0] == '.' {
					continue
				}
				candidates = append(candidates, []string{"/tmp", volPath})
			}
		}

		path1, path2 = findDifferentFilesystemPaths(candidates)

	case "linux":
		// Linux: Try common mount points
		candidates := [][]string{
			{"/", "/tmp"},
			{"/", "/var"},
			{"/", "/boot"},
			{"/", "/proc"},
			{"/", "/sys"},
			{"/tmp", "/var"},
			{"/tmp", "/boot"},
		}

		path1, path2 = findDifferentFilesystemPaths(candidates)

	default:
		// For other OS, try some common paths
		candidates := [][]string{
			{"/", "/tmp"},
			{"/tmp", "/var"},
		}

		path1, path2 = findDifferentFilesystemPaths(candidates)
	}

	if path1 == "" || path2 == "" {
		t.Skip("Could not find different filesystems on this system")
	}

	t.Logf("Testing different filesystems: %s vs %s", path1, path2)

	testPath1 := filepath.Join(path1, "test_file1.txt")
	testPath2 := filepath.Join(path2, "test_file2.txt")

	same, err := isSameFilesystem(testPath1, testPath2)
	require.NoError(t, err)
	assert.False(t, same, "Paths on different filesystems should return false")
}

// findDifferentFilesystemPaths takes a list of directory path pairs and returns
// the first pair that are on different filesystems, or empty strings if none found.
func findDifferentFilesystemPaths(candidates [][]string) (string, string) {
	for _, pair := range candidates {
		dir1, dir2 := pair[0], pair[1]

		// Check if both directories exist
		stat1, err1 := os.Stat(dir1)
		if err1 != nil {
			continue
		}
		stat2, err2 := os.Stat(dir2)
		if err2 != nil {
			continue
		}

		// Check if they're on different filesystems
		sys1, ok1 := stat1.Sys().(*syscall.Stat_t)
		sys2, ok2 := stat2.Sys().(*syscall.Stat_t)

		if !ok1 || !ok2 {
			continue // Can't get device info, skip this pair
		}

		if sys1.Dev != sys2.Dev {
			// Found different filesystems!
			return dir1, dir2
		}
	}

	return "", "" // No different filesystems found
}

func TestIsSameFilesystem_ForceFalse(t *testing.T) {
	// Test that the force false variable works
	tempDir := t.TempDir()
	path1 := filepath.Join(tempDir, "file1.txt")
	path2 := filepath.Join(tempDir, "file2.txt")

	// First verify they would normally be on the same filesystem
	same, err := isSameFilesystem(path1, path2)
	require.NoError(t, err)
	assert.True(t, same, "Files in same temp dir should be on same filesystem")

	// Now test with force false enabled
	originalValue := IsSameFileSystemForTests_ForceFalse
	defer func() { IsSameFileSystemForTests_ForceFalse = originalValue }() // Restore after test

	IsSameFileSystemForTests_ForceFalse = true

	same, err = isSameFilesystem(path1, path2)
	require.NoError(t, err)
	assert.False(t, same, "Should return false when IsSameFileSystemForTests_ForceFalse is true")
}
