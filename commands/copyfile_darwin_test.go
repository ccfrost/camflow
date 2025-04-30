//go:build darwin
// +build darwin

package commands

import (
	"errors"
	"os"
	"path/filepath" // Import strings package
	"syscall"
	"testing"

	"github.com/pkg/xattr" // Import the xattr package
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopyFileMacOS_Success(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	srcFile := filepath.Join(srcDir, "source.txt")
	dstFile := filepath.Join(dstDir, "destination.txt")
	content := []byte("hello world from copyfile test")
	testAttrName := "com.example.testattr"
	testAttrValue := "test_value"
	testMode := os.FileMode(0751) // Example specific permissions

	// Create source file
	err := os.WriteFile(srcFile, content, 0644) // Initial permissive mode
	require.NoError(t, err, "Failed to create source file")

	// Set specific permissions on source
	err = os.Chmod(srcFile, testMode)
	require.NoError(t, err, "Failed to chmod source file")

	// Set extended attribute on source
	err = xattr.Set(srcFile, testAttrName, []byte(testAttrValue))
	require.NoError(t, err, "Failed to set xattr on source file using xattr package")

	// Perform the copy
	err = copyFileMacOS(srcFile, dstFile)
	assert.NoError(t, err, "copyFileMacOS failed unexpectedly")

	// Verify destination file exists
	dstInfo, err := os.Stat(dstFile)
	require.NoError(t, err, "Destination file does not exist after copy")
	assert.False(t, dstInfo.IsDir(), "Destination should be a file, not a directory")

	// Verify destination file content
	dstContent, err := os.ReadFile(dstFile)
	require.NoError(t, err, "Failed to read destination file")
	assert.Equal(t, content, dstContent, "Destination file content does not match source")

	// Verify permissions are preserved
	srcInfo, err := os.Stat(srcFile) // Re-stat source to be sure
	require.NoError(t, err)
	assert.Equal(t, srcInfo.Mode().Perm(), dstInfo.Mode().Perm(), "File permissions not preserved")
	assert.Equal(t, testMode.Perm(), dstInfo.Mode().Perm(), "Destination permissions do not match expected test mode")

	// Verify extended attribute is preserved
	outputBytes, err := xattr.Get(dstFile, testAttrName)
	require.NoError(t, err, "Failed to get xattr from destination file using xattr package")
	assert.Equal(t, testAttrValue, string(outputBytes), "Extended attribute not preserved or value mismatch")
}

func TestCopyFileMacOS_SourceNotExist(t *testing.T) {
	srcFile := filepath.Join(t.TempDir(), "nonexistent_source.txt")
	dstFile := filepath.Join(t.TempDir(), "destination.txt")

	err := copyFileMacOS(srcFile, dstFile)
	require.Error(t, err, "copyFileMacOS should have failed for non-existent source")

	// Check if the error is specifically ENOENT (No such file or directory)
	var errno syscall.Errno
	require.True(t, errors.As(err, &errno), "Error should be a syscall.Errno")
	assert.Equal(t, syscall.ENOENT, errno, "Expected ENOENT error for non-existent source")

	// Ensure destination file was not created
	_, err = os.Stat(dstFile)
	assert.True(t, os.IsNotExist(err), "Destination file should not have been created")
}

func TestCopyFileMacOS_DestDirNotExist(t *testing.T) {
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "source.txt")
	dstFile := filepath.Join(t.TempDir(), "nonexistent_dir", "destination.txt") // Parent dir doesn't exist
	content := []byte("test content")

	err := os.WriteFile(srcFile, content, 0644)
	require.NoError(t, err, "Failed to create source file")

	err = copyFileMacOS(srcFile, dstFile)
	require.Error(t, err, "copyFileMacOS should have failed for non-existent destination directory")

	// Check if the error is specifically ENOENT (No such file or directory)
	// copyfile itself tries to create the destination file, so the error comes from that attempt.
	var errno syscall.Errno
	require.True(t, errors.As(err, &errno), "Error should be a syscall.Errno")
	assert.Equal(t, syscall.ENOENT, errno, "Expected ENOENT error for non-existent destination directory")

	// Ensure destination file was not created
	_, err = os.Stat(dstFile)
	assert.True(t, os.IsNotExist(err), "Destination file should not have been created")
}

func TestCopyFileMacOS_DestIsDirectory(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir() // This directory already exists

	srcFile := filepath.Join(srcDir, "source.txt")
	content := []byte("test content")

	err := os.WriteFile(srcFile, content, 0644)
	require.NoError(t, err, "Failed to create source file")

	// Attempt to copy the file *onto* the existing directory path
	err = copyFileMacOS(srcFile, dstDir)
	require.Error(t, err, "copyFileMacOS should have failed when destination is a directory")

	// Check if the error is specifically EISDIR (Is a directory)
	var errno syscall.Errno
	require.True(t, errors.As(err, &errno), "Error should be a syscall.Errno")
	// On macOS, copyfile returns EEXIST if the destination exists and is a directory
	// when not using specific flags to overwrite or merge. Let's check for EEXIST or EISDIR.
	assert.True(t, errno == syscall.EEXIST || errno == syscall.EISDIR, "Expected EEXIST or EISDIR error when destination is a directory, got %v", errno)
}
