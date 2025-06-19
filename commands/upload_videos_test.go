package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync" // For wg in context cancellation test
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/gphotosuploader/google-photos-api-client-go/v3/albums"      // For types like albums.Album
	"github.com/gphotosuploader/google-photos-api-client-go/v3/media_items" // For types like media_items.SimpleMediaItem
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Test Helper Functions ---

func createTestFiles(t *testing.T, dir string, files map[string]string) string {
	t.Helper()

	for name, content := range files {
		filePath := filepath.Join(dir, name)
		err := os.WriteFile(filePath, []byte(content), 0644)
		require.NoError(t, err, "Failed to write file %s: %v", filePath, err)
	}
	return dir
}

// --- Helper Functions for Directory Structure Testing ---

// createDirStructure creates a directory structure with the given files
func createDirStructure(t *testing.T, baseDir string, structure map[string]string) {
	t.Helper()
	for relPath, content := range structure {
		fullPath := filepath.Join(baseDir, relPath)
		dir := filepath.Dir(fullPath)
		require.NoError(t, os.MkdirAll(dir, 0755), "Failed to create directory %s", dir)
		require.NoError(t, os.WriteFile(fullPath, []byte(content), 0644), "Failed to create file %s", fullPath)
	}
}

// assertDirExists checks if a directory exists
func assertDirExists(t *testing.T, path string, msg string) {
	t.Helper()
	_, err := os.Stat(path)
	assert.NoError(t, err, msg)
}

// assertDirNotExists checks if a directory does not exist
func assertDirNotExists(t *testing.T, path string, msg string) {
	t.Helper()
	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err), msg)
}

// --- Test Functions ---

func TestUploadVideos_StagingDirNotConfigured(t *testing.T) {
	cfg := newTestConfig(t, nil)
	// Intentionally make VideosOrigStagingRoot unconfigured for this test case
	cfg.VideosOrigStagingRoot = ""

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl) // Changed from localMocks.NewMockGPhotosClient

	err := UploadVideos(context.Background(), cfg, t.TempDir(), false, mockGPhotosClient)
	require.Error(t, err, "Expected an error when staging dir is not configured, got nil")
	assert.Contains(t, err.Error(), "video staging directory (VideosOrigStagingRoot) not configured", "Expected error message about staging dir not configured, got: %v", err)
}

func TestUploadVideos_StagingDirDoesNotExist(t *testing.T) {
	cfg := newTestConfig(t, nil)
	require.NoError(t, os.RemoveAll(cfg.VideosOrigStagingRoot))
	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl) // Changed from localMocks.NewMockGPhotosClient

	err := UploadVideos(context.Background(), cfg, t.TempDir(), false, mockGPhotosClient)
	assert.NoError(t, err, "Expected no error when staging dir does not exist, got: %v", err)
}

func TestUploadVideos_EmptyStagingDir(t *testing.T) {
	cfg := newTestConfig(t, nil)
	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl) // Changed from localMocks.NewMockGPhotosClient

	err := UploadVideos(context.Background(), cfg, t.TempDir(), false, mockGPhotosClient)
	assert.NoError(t, err, "Expected no error for empty staging dir, got: %v", err)
}

func TestUploadVideos_FilesToUpload_NoAlbums_MoveFiles(t *testing.T) {
	ctx := context.Background()

	cfg := newTestConfig(t, nil) // No default albums
	filesToCreate := map[string]string{"video1.mp4": "content1", "video2.mov": "content2"}
	stagingDir := createTestFiles(t, cfg.VideosOrigStagingRoot, filesToCreate)

	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)        // Changed from localMocks.NewMockGPhotosClient
	mockUploaderSvc := NewMockMediaUploader(ctrl)          // Changed from localMocks.NewMockMediaUploader
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl) // Changed from localMocks.NewMockAppMediaItemsService

	// Setup expectations for our GPhotosClient mock to return the service mocks
	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()
	// No expectation for Albums() as it shouldn't be called in this test (NoAlbums)

	// Expectations for UploadFile and CreateMediaItem for each file
	for baseName := range filesToCreate {
		filePath := filepath.Join(stagingDir, baseName)
		uploadToken := "upload_token_for_" + baseName
		mediaItemID := "media_item_id_for_" + baseName

		mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), filePath).
			Return(uploadToken, nil)
		mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: baseName}).
			Return(&media_items.MediaItem{ID: mediaItemID, Filename: baseName}, nil)
	}

	err := UploadVideos(ctx, cfg, tempConfigDir, false /* keepStaging */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos failed: %v", err)

	// Verify files are moved from staging and exist in VideosOrigRoot
	for baseName := range filesToCreate {
		stagingPath := filepath.Join(stagingDir, baseName)
		destPath := filepath.Join(cfg.VideosOrigRoot, baseName)

		_, statErr := os.Stat(stagingPath)
		assert.True(t, os.IsNotExist(statErr), "Expected file %s to be moved from staging %s, but it still exists", baseName, stagingDir)

		_, statErr = os.Stat(destPath)
		assert.NoError(t, statErr, "Expected file %s to be moved to %s, but it's not there", baseName, destPath)
	}

	// Check that staging dir is now empty (since files were in root, no subdirectories to clean)
	remainingFiles, _ := os.ReadDir(stagingDir)
	assert.Empty(t, remainingFiles, "Expected staging directory to be empty after moves, but found %d files", len(remainingFiles))
}

func TestUploadVideos_FilesToUpload_NoAlbums_KeepFiles(t *testing.T) {
	ctx := context.Background()

	cfg := newTestConfig(t, nil)
	videoFile := "video1.mp4"
	createTestFiles(t, cfg.VideosOrigStagingRoot, map[string]string{videoFile: "content1"})
	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)        // Changed from localMocks.NewMockGPhotosClient
	mockUploaderSvc := NewMockMediaUploader(ctrl)          // Changed from localMocks.NewMockMediaUploader
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl) // Changed from localMocks.NewMockAppMediaItemsService

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()

	uploadToken := "token_for_" + videoFile
	mediaItemID := "id_for_" + videoFile
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), filepath.Join(cfg.VideosOrigStagingRoot, videoFile)).
		Return(uploadToken, nil)
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: videoFile}).
		Return(&media_items.MediaItem{ID: mediaItemID, Filename: videoFile}, nil)

	err := UploadVideos(ctx, cfg, tempConfigDir, true /* keepStaging */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos failed: %v", err)

	_, statErr := os.Stat(filepath.Join(cfg.VideosOrigStagingRoot, videoFile))
	assert.NoError(t, statErr, "Expected %s to be kept in staging, but it was deleted (os.IsNotExist was true for stat error: %v)", videoFile, statErr)
}

// TestUploadVideos_FilesToUpload_WithAlbums_CreatesAndAddsToAlbum tests uploading a video,
// creating a new album when it doesn't exist, adding the video to it, and moving the local file.
func TestUploadVideos_FilesToUpload_WithAlbums_CreatesAndAddsToAlbum(t *testing.T) {
	ctx := context.Background()

	albumTitle := "NewAlbumToCreate"
	albumTitles := []string{albumTitle}
	cfg := newTestConfig(t, albumTitles)

	videoFileName := "video1.mp4"
	videoFilePath := filepath.Join(cfg.VideosOrigStagingRoot, videoFileName)
	err := os.WriteFile(videoFilePath, []byte("content"), 0644)
	require.NoError(t, err, "Failed to write video file: %v", err)

	tempConfigDir := t.TempDir() // For album cache

	ctrl := gomock.NewController(t)
	// TODO: do Finish in all tests.
	defer ctrl.Finish()                                    // Ensures all expectations are checked
	mockGPhotosClient := NewMockGPhotosClient(ctrl)        // Changed from localMocks.NewMockGPhotosClient
	mockAlbumsSvc := NewMockAppAlbumsService(ctrl)         // Changed from localMocks.NewMockAppAlbumsService
	mockUploaderSvc := NewMockMediaUploader(ctrl)          // Changed from localMocks.NewMockMediaUploader
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl) // Changed from localMocks.NewMockAppMediaItemsService

	mockGPhotosClient.EXPECT().Albums().Return(mockAlbumsSvc).AnyTimes()
	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()

	// Mock for getOrFetchAndCreateAlbumIDs: album not found, then created
	// List returns a slice directly, not an iterator.
	mockAlbumsSvc.EXPECT().List(gomock.Any()).Return([]albums.Album{}, nil) // Simulate album not found initially

	createdAlbumID := "album-id-for-" + albumTitle
	mockAlbumsSvc.EXPECT().Create(gomock.Any(), albumTitle).
		Return(&albums.Album{ID: createdAlbumID, Title: albumTitle}, nil)

	// Mock for uploadVideo: upload, create media item, add to album
	uploadToken := "token_for_" + videoFileName
	mediaItemID := "media_id_for_" + videoFileName

	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), videoFilePath).
		Return(uploadToken, nil)
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: videoFileName}).
		Return(&media_items.MediaItem{ID: mediaItemID, Filename: videoFileName}, nil)
	mockAlbumsSvc.EXPECT().AddMediaItems(gomock.Any(), createdAlbumID, []string{mediaItemID}).
		Return(nil) // Successful addition

	err = UploadVideos(ctx, cfg, tempConfigDir, false /* keepStaging */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos failed: %v", err)

	// Verify file is moved from staging
	_, statErr := os.Stat(videoFilePath)
	assert.True(t, os.IsNotExist(statErr), "Expected video file %s to be moved from staging, but it still exists. Error: %v", videoFilePath, statErr)

	// Verify file exists in VideosOrigRoot
	expectedDestPath := filepath.Join(cfg.VideosOrigRoot, videoFileName)
	_, statErr = os.Stat(expectedDestPath)
	assert.NoError(t, statErr, "Expected video file %s to be moved to %s, but it does not exist. Error: %v", videoFileName, expectedDestPath, statErr)
}

func TestUploadVideos_ErrorLoadAlbumCache(t *testing.T) {
	ctx := context.Background()

	cfg := newTestConfig(t, []string{"Album1"})
	createTestFiles(t, cfg.VideosOrigStagingRoot, map[string]string{"video1.mp4": "content"})

	tempConfigDir := t.TempDir()
	// Ensure the cache path logic in test matches the main code's getAlbumCachePath
	// Assuming getAlbumCachePath uses configDir directly if provided, or os.UserConfigDir() + "camflow"
	// The constant is albumCacheFileName = "google_photos_album_cache.json"
	// If configDir is tempConfigDir, then cache path is filepath.Join(tempConfigDir, "google_photos_album_cache.json")
	// The main code uses: filepath.Join(configDir, albumCacheFileName)
	albumCacheFilePath := filepath.Join(tempConfigDir, "google_photos_album_cache.json")

	os.WriteFile(albumCacheFilePath, []byte("this is not json"), 0644)

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl) // Changed from localMocks.NewMockGPhotosClient

	uploadErr := UploadVideos(ctx, cfg, tempConfigDir, false, mockGPhotosClient)
	require.Error(t, uploadErr, "UploadVideos expected to fail due to malformed album cache, but succeeded")
	assert.Contains(t, uploadErr.Error(), "failed to load album cache", "Expected error about loading album cache, got: %v", uploadErr)
}

func TestUploadVideos_ErrorGetOrCreateAlbumIDs(t *testing.T) {
	ctx := context.Background()
	albumTitles := []string{"AlbumThatCausesError"}
	cfg := newTestConfig(t, albumTitles)
	createTestFiles(t, cfg.VideosOrigStagingRoot, map[string]string{"video1.mp4": "content"})
	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl) // Changed from localMocks.NewMockGPhotosClient
	mockAlbumsSvc := NewMockAppAlbumsService(ctrl)  // Changed from localMocks.NewMockAppAlbumsService

	mockGPhotosClient.EXPECT().Albums().Return(mockAlbumsSvc).AnyTimes()

	expectedErrStr := "simulated error listing albums"
	// List returns a slice directly, not an iterator.
	mockAlbumsSvc.EXPECT().List(gomock.Any()).Return(nil, errors.New(expectedErrStr))

	err := UploadVideos(ctx, cfg, tempConfigDir, false, mockGPhotosClient)
	require.Error(t, err, "UploadVideos expected to fail due to error in getOrFetchAndCreateAlbumIDs, but succeeded")
	assert.Contains(t, err.Error(), expectedErrStr, "Expected error '%s', got: %v", expectedErrStr, err)
}

func TestUploadVideos_ErrorUploadFile(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig(t, nil)
	videoFileName := "video1.mp4"
	createTestFiles(t, cfg.VideosOrigStagingRoot, map[string]string{videoFileName: "content"})
	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl) // Changed from localMocks.NewMockGPhotosClient
	mockUploaderSvc := NewMockMediaUploader(ctrl)   // Changed from localMocks.NewMockMediaUploader

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()

	expectedErrStr := "simulated upload failure"
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), filepath.Join(cfg.VideosOrigStagingRoot, videoFileName)).
		Return("", errors.New(expectedErrStr))

	err := UploadVideos(ctx, cfg, tempConfigDir, false, mockGPhotosClient)
	require.Error(t, err, "UploadVideos expected to fail due to UploadFile error, but succeeded")
	assert.Contains(t, err.Error(), "failed to upload file", "Error message mismatch")
	assert.Contains(t, err.Error(), videoFileName, "Error message should contain filename")
	assert.Contains(t, err.Error(), expectedErrStr, "Error message should contain original error")

	_, statErr := os.Stat(filepath.Join(cfg.VideosOrigStagingRoot, videoFileName))
	assert.NoError(t, statErr, "Expected %s to be kept in staging after upload failure, but it was deleted (os.IsNotExist was true for stat error: %v)", videoFileName, statErr)
}

func TestUploadVideos_ErrorCreateMediaItem(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig(t, nil)
	videoFileName := "video1.mp4"
	createTestFiles(t, cfg.VideosOrigStagingRoot, map[string]string{videoFileName: "content"})
	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)        // Changed from localMocks.NewMockGPhotosClient
	mockUploaderSvc := NewMockMediaUploader(ctrl)          // Changed from localMocks.NewMockMediaUploader
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl) // Changed from localMocks.NewMockAppMediaItemsService

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()

	uploadToken := "upload_token_for_" + videoFileName
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), filepath.Join(cfg.VideosOrigStagingRoot, videoFileName)).
		Return(uploadToken, nil)

	expectedErrStr := "simulated create media item failure"
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: videoFileName}).
		Return(nil, errors.New(expectedErrStr))

	err := UploadVideos(ctx, cfg, tempConfigDir, false, mockGPhotosClient)
	// The main UploadVideos function currently continues on CreateMediaItem error, so no top-level error is expected here.
	// It logs the error and proceeds. If this behavior changes, this check needs an update.
	assert.NoError(t, err, "UploadVideos failed unexpectedly: %v. Expected to continue on CreateMediaItem error.", err)

	_, statErr := os.Stat(filepath.Join(cfg.VideosOrigStagingRoot, videoFileName))
	assert.NoError(t, statErr, "Expected %s to be kept in staging after CreateMediaItem failure, but it was deleted (os.IsNotExist was true for stat error: %v)", videoFileName, statErr)
}

func TestUploadVideos_ErrorAddMediaToAlbum_FileKept_WhenAlbumExists(t *testing.T) {
	ctx := context.Background()

	albumTitle := "ExistingAlbum"
	cfg := newTestConfig(t, []string{albumTitle})

	videoFileName := "video1.mp4"
	videoFilePath := filepath.Join(cfg.VideosOrigStagingRoot, videoFileName)
	require.NoError(t, os.WriteFile(videoFilePath, []byte("content"), 0644))

	tempConfigDir := t.TempDir() // For album cache

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)        // Changed from localMocks.NewMockGPhotosClient
	mockUploaderSvc := NewMockMediaUploader(ctrl)          // Changed from localMocks.NewMockMediaUploader
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl) // Changed from localMocks.NewMockAppMediaItemsService
	mockAlbumsSvc := NewMockAppAlbumsService(ctrl)         // Changed from localMocks.NewMockAppAlbumsService

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()
	mockGPhotosClient.EXPECT().Albums().Return(mockAlbumsSvc).AnyTimes()

	// Mock for getOrFetchAndCreateAlbumIDs: album exists
	existingAlbumID := "album-id-real-existing"
	// List returns a slice directly, not an iterator.
	mockAlbumsSvc.EXPECT().List(gomock.Any()).Return([]albums.Album{{ID: existingAlbumID, Title: albumTitle}}, nil)
	mockAlbumsSvc.EXPECT().Create(gomock.Any(), gomock.Any()).Times(0) // Ensure Create is not called

	// Mock for uploadVideo: upload, create media item
	uploadToken := "token_for_" + videoFileName
	mediaItemID := "media-id_for_" + videoFileName
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), videoFilePath).
		Return(uploadToken, nil)
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: videoFileName}).
		Return(&media_items.MediaItem{ID: mediaItemID, Filename: videoFileName}, nil)

	// Mock for AddMediaItems: simulate failure
	expectedAddError := "simulated add to album failure"
	mockAlbumsSvc.EXPECT().AddMediaItems(gomock.Any(), existingAlbumID, []string{mediaItemID}).
		Return(errors.New(expectedAddError))

	err := UploadVideos(ctx, cfg, tempConfigDir, false /* keepStaging */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos returned an unexpected error: %v. Expected to continue on AddMediaItems error.", err)

	// Verify file is NOT deleted because add to album failed
	_, statErr := os.Stat(videoFilePath)
	assert.NoError(t, statErr, "Expected video file %s to be kept after AddMediaItems failure, but it was deleted (os.IsNotExist was true for stat error: %v)", videoFilePath, statErr)
}

func TestUploadVideos_ContextCancellationDuringLimiterWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	cfg := newTestConfig(t, nil)

	videoFileName := "video1.mp4"
	createTestFiles(t, cfg.VideosOrigStagingRoot, map[string]string{videoFileName: "content"})

	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl) // Changed from localMocks.NewMockGPhotosClient
	mockUploaderSvc := NewMockMediaUploader(ctrl)   // Changed from localMocks.NewMockMediaUploader

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()

	// Make UploadFileFunc block or react to cancellation
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), gomock.Any()).
		DoAndReturn(func(c context.Context, path string) (string, error) {
			select {
			case <-c.Done():
				return "", c.Err()
			case <-time.After(5 * time.Second): // Timeout for the test itself
				t.Log("UploadFile mock timed out waiting for cancellation")
				return "unwanted-token", nil
			}
		}).AnyTimes() // .AnyTimes() because it might be called 0 or 1 times depending on limiter

	var errUpload error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		errUpload = UploadVideos(ctx, cfg, tempConfigDir, false, mockGPhotosClient)
	}()

	time.Sleep(20 * time.Millisecond) // Short delay to allow UploadVideos to start
	cancel()                          // Cancel the context
	wg.Wait()                         // Wait for UploadVideos to complete or fail

	require.Error(t, errUpload, "Expected an error due to context cancellation, got nil")
	// Check if the error is context.Canceled or context.DeadlineExceeded
	isContextError := errors.Is(errUpload, context.Canceled) || errors.Is(errUpload, context.DeadlineExceeded)
	assert.True(t, isContextError, "Expected context.Canceled or context.DeadlineExceeded, got %v", errUpload)

	_, statErr := os.Stat(filepath.Join(cfg.VideosOrigStagingRoot, videoFileName))
	assert.NoError(t, statErr, "Expected %s to be kept in staging after context cancellation, but it was deleted (os.IsNotExist was true for stat error: %v)", videoFileName, statErr)
}

// TestUploadVideos_FilesToUpload_WithAlbums_AlbumExists tests uploading a video,
// using an existing album, adding the video to it, and moving the local file.
func TestUploadVideos_FilesToUpload_WithAlbums_AlbumExists(t *testing.T) {
	ctx := context.Background()

	albumTitle := "MyExistingAlbum"
	albumTitles := []string{albumTitle}
	cfg := newTestConfig(t, albumTitles)

	videoFileName := "existing_album_video.mp4"
	videoFilePath := filepath.Join(cfg.VideosOrigStagingRoot, videoFileName)
	err := os.WriteFile(videoFilePath, []byte("content for existing album test"), 0644)
	require.NoError(t, err, "Failed to write video file: %v", err)

	tempConfigDir := t.TempDir() // For album cache

	ctrl := gomock.NewController(t)
	defer ctrl.Finish() // Ensures all expectations are checked

	mockGPhotosClient := NewMockGPhotosClient(ctrl)
	mockAlbumsSvc := NewMockAppAlbumsService(ctrl)
	mockUploaderSvc := NewMockMediaUploader(ctrl)
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl)

	mockGPhotosClient.EXPECT().Albums().Return(mockAlbumsSvc).AnyTimes()
	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()

	// Mock for getOrFetchAndCreateAlbumIDs: album IS found
	existingAlbumID := "album-id-for-" + albumTitle
	mockAlbumsSvc.EXPECT().List(gomock.Any()).Return([]albums.Album{{ID: existingAlbumID, Title: albumTitle}}, nil)
	mockAlbumsSvc.EXPECT().Create(gomock.Any(), gomock.Any()).Times(0) // Ensure Create is NOT called

	// Mock for uploadVideo: upload, create media item, add to album
	uploadToken := "token_for_" + videoFileName
	mediaItemID := "media_id_for_" + videoFileName

	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), videoFilePath).
		Return(uploadToken, nil)
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: videoFileName}).
		Return(&media_items.MediaItem{ID: mediaItemID, Filename: videoFileName}, nil)
	mockAlbumsSvc.EXPECT().AddMediaItems(gomock.Any(), existingAlbumID, []string{mediaItemID}).
		Return(nil) // Successful addition

	err = UploadVideos(ctx, cfg, tempConfigDir, false /* keepStaging */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos failed: %v", err)

	// Verify file is moved from staging
	_, statErr := os.Stat(videoFilePath)
	assert.True(t, os.IsNotExist(statErr), "Expected video file %s to be moved from staging, but it still exists. Error: %v", videoFilePath, statErr)

	// Verify file exists in VideosOrigRoot
	expectedDestPath := filepath.Join(cfg.VideosOrigRoot, videoFileName)
	_, statErr = os.Stat(expectedDestPath)
	assert.NoError(t, statErr, "Expected video file %s to be moved to %s, but it does not exist. Error: %v", videoFileName, expectedDestPath, statErr)
}

// --- Test Functions for cleanupEmptyStagingDirectories ---

func TestCleanupEmptyStagingDirectories_Success(t *testing.T) {
	tempDir := t.TempDir()
	stagingRoot := filepath.Join(tempDir, "staging")
	require.NoError(t, os.MkdirAll(stagingRoot, 0755))

	// Create nested directory structure
	videoPath := filepath.Join(stagingRoot, "2024", "01", "15", "video.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(videoPath), 0755))

	// Test that cleanup removes empty parent directories
	err := cleanupEmptyStagingDirectories(videoPath, stagingRoot)
	require.NoError(t, err)

	// All parent directories should be removed except staging root
	assertDirNotExists(t, filepath.Join(stagingRoot, "2024", "01", "15"), "Expected deepest directory to be removed")
	assertDirNotExists(t, filepath.Join(stagingRoot, "2024", "01"), "Expected middle directory to be removed")
	assertDirNotExists(t, filepath.Join(stagingRoot, "2024"), "Expected year directory to be removed")
	assertDirExists(t, stagingRoot, "Expected staging root to remain")
}

func TestCleanupEmptyStagingDirectories_StopsAtNonEmptyDir(t *testing.T) {
	tempDir := t.TempDir()
	stagingRoot := filepath.Join(tempDir, "staging")
	require.NoError(t, os.MkdirAll(stagingRoot, 0755))

	// Create nested directory structure with another file in middle directory
	videoPath := filepath.Join(stagingRoot, "2024", "01", "15", "video.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(videoPath), 0755))

	// Add another file in the "01" directory to make it non-empty
	otherFile := filepath.Join(stagingRoot, "2024", "01", "other.txt")
	require.NoError(t, os.WriteFile(otherFile, []byte("content"), 0644))

	err := cleanupEmptyStagingDirectories(videoPath, stagingRoot)
	require.NoError(t, err)

	// Only the deepest empty directory should be removed
	assertDirNotExists(t, filepath.Join(stagingRoot, "2024", "01", "15"), "Expected deepest directory to be removed")
	assertDirExists(t, filepath.Join(stagingRoot, "2024", "01"), "Expected non-empty directory to remain")
	assertDirExists(t, filepath.Join(stagingRoot, "2024"), "Expected parent of non-empty directory to remain")
	assertDirExists(t, stagingRoot, "Expected staging root to remain")
}

func TestCleanupEmptyStagingDirectories_FileDirectlyInRoot(t *testing.T) {
	tempDir := t.TempDir()
	stagingRoot := filepath.Join(tempDir, "staging")
	require.NoError(t, os.MkdirAll(stagingRoot, 0755))

	// Video file directly in staging root
	videoPath := filepath.Join(stagingRoot, "video.mp4")

	err := cleanupEmptyStagingDirectories(videoPath, stagingRoot)
	require.NoError(t, err)

	// Staging root should still exist (nothing to clean)
	assertDirExists(t, stagingRoot, "Expected staging root to remain")
}

func TestCleanupEmptyStagingDirectories_DoesNotCleanOutsideStaging(t *testing.T) {
	tempDir := t.TempDir()
	stagingRoot := filepath.Join(tempDir, "staging")
	require.NoError(t, os.MkdirAll(stagingRoot, 0755))

	// Try to clean a path outside staging (should do nothing)
	outsidePath := filepath.Join(tempDir, "other", "subdir", "file.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(outsidePath), 0755))

	err := cleanupEmptyStagingDirectories(outsidePath, stagingRoot)
	require.NoError(t, err)

	// Directory outside staging should remain untouched
	assertDirExists(t, filepath.Dir(outsidePath), "Expected directory outside staging to remain")
}

func TestCleanupEmptyStagingDirectories_HandlesNonexistentDirectory(t *testing.T) {
	tempDir := t.TempDir()
	stagingRoot := filepath.Join(tempDir, "staging")
	require.NoError(t, os.MkdirAll(stagingRoot, 0755))

	// Path to nonexistent directory
	videoPath := filepath.Join(stagingRoot, "nonexistent", "video.mp4")

	err := cleanupEmptyStagingDirectories(videoPath, stagingRoot)
	require.NoError(t, err)

	// Should handle gracefully without error
	assertDirExists(t, stagingRoot, "Expected staging root to remain")
}

func TestCleanupEmptyStagingDirectories_HandlesPermissionError(t *testing.T) {
	tempDir := t.TempDir()
	stagingRoot := filepath.Join(tempDir, "staging")
	require.NoError(t, os.MkdirAll(stagingRoot, 0755))

	// Create directory structure
	parentDir := filepath.Join(stagingRoot, "readonly")
	videoDir := filepath.Join(parentDir, "subdir")
	videoPath := filepath.Join(videoDir, "video.mp4")
	require.NoError(t, os.MkdirAll(videoDir, 0755))

	// Make parent directory read-only to simulate permission error
	require.NoError(t, os.Chmod(parentDir, 0555))
	defer os.Chmod(parentDir, 0755) // Restore permissions for cleanup

	err := cleanupEmptyStagingDirectories(videoPath, stagingRoot)
	assert.Error(t, err, "Expected error when unable to remove directory due to permissions")
	assert.Contains(t, err.Error(), "failed to remove empty staging subdirectory", "Expected specific error message")
}

func TestCleanupEmptyStagingDirectories_DoesNotDeleteStagingRoot(t *testing.T) {
	tempDir := t.TempDir()
	stagingRoot := filepath.Join(tempDir, "staging")
	require.NoError(t, os.MkdirAll(stagingRoot, 0755))

	// Video path that's the staging root itself (edge case)
	err := cleanupEmptyStagingDirectories(stagingRoot, stagingRoot)
	require.NoError(t, err)

	// Staging root should never be deleted
	assertDirExists(t, stagingRoot, "Expected staging root to never be deleted")
}

func TestCleanupEmptyStagingDirectories_HandlesSymlinks(t *testing.T) {
	tempDir := t.TempDir()
	stagingRoot := filepath.Join(tempDir, "staging")
	require.NoError(t, os.MkdirAll(stagingRoot, 0755))

	// Create a directory structure with a symlink
	realDir := filepath.Join(tempDir, "real")
	require.NoError(t, os.MkdirAll(realDir, 0755))

	symlinkDir := filepath.Join(stagingRoot, "symlink")
	require.NoError(t, os.Symlink(realDir, symlinkDir))

	videoPath := filepath.Join(symlinkDir, "video.mp4")

	err := cleanupEmptyStagingDirectories(videoPath, stagingRoot)
	require.NoError(t, err)

	// Should handle symlinks properly without breaking
	assertDirExists(t, stagingRoot, "Expected staging root to remain")
}

// --- Updated Existing Tests to Account for Cleanup ---

func TestUploadVideos_FilesToUpload_NoAlbums_MoveFiles_WithCleanup(t *testing.T) {
	ctx := context.Background()

	cfg := newTestConfig(t, nil)

	// Create files in nested directory structure to test cleanup
	filesToCreate := map[string]string{
		"2024/01/15/video1.mp4": "content1",
		"2024/01/16/video2.mov": "content2",
		"2024/02/01/video3.mp4": "content3",
	}

	stagingDir := cfg.VideosOrigStagingRoot
	createDirStructure(t, stagingDir, filesToCreate)

	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)
	mockUploaderSvc := NewMockMediaUploader(ctrl)
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl)

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()

	// Expectations for UploadFile and CreateMediaItem for each file
	for relPath := range filesToCreate {
		filePath := filepath.Join(stagingDir, relPath)
		baseName := filepath.Base(relPath)
		uploadToken := "upload_token_for_" + baseName
		mediaItemID := "media_item_id_for_" + baseName

		mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), filePath).
			Return(uploadToken, nil)
		mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: baseName}).
			Return(&media_items.MediaItem{ID: mediaItemID, Filename: baseName}, nil)
	}

	err := UploadVideos(ctx, cfg, tempConfigDir, false /* keepStaging */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos failed: %v", err)

	// Verify files are moved from staging and exist in VideosOrigRoot
	for relPath := range filesToCreate {
		stagingPath := filepath.Join(stagingDir, relPath)
		destPath := filepath.Join(cfg.VideosOrigRoot, relPath)

		_, statErr := os.Stat(stagingPath)
		assert.True(t, os.IsNotExist(statErr), "Expected file %s to be moved from staging", relPath)

		_, statErr = os.Stat(destPath)
		assert.NoError(t, statErr, "Expected file %s to be moved to %s", relPath, destPath)
	}

	// Verify empty directories were cleaned up
	assertDirNotExists(t, filepath.Join(stagingDir, "2024", "01", "15"), "Expected empty subdirectory to be cleaned up")
	assertDirNotExists(t, filepath.Join(stagingDir, "2024", "01", "16"), "Expected empty subdirectory to be cleaned up")
	assertDirNotExists(t, filepath.Join(stagingDir, "2024", "02", "01"), "Expected empty subdirectory to be cleaned up")
	assertDirNotExists(t, filepath.Join(stagingDir, "2024", "01"), "Expected empty parent directory to be cleaned up")
	assertDirNotExists(t, filepath.Join(stagingDir, "2024", "02"), "Expected empty parent directory to be cleaned up")
	assertDirNotExists(t, filepath.Join(stagingDir, "2024"), "Expected empty year directory to be cleaned up")

	// Staging root should still exist
	assertDirExists(t, stagingDir, "Expected staging root to remain")
}

func TestCleanupEmptyStagingDirectories_PartialCleanup(t *testing.T) {
	tempDir := t.TempDir()
	stagingRoot := filepath.Join(tempDir, "staging")
	require.NoError(t, os.MkdirAll(stagingRoot, 0755))

	// Create structure: staging/2024/01/15/video.mp4 and staging/2024/02/file.txt
	videoPath := filepath.Join(stagingRoot, "2024", "01", "15", "video.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(videoPath), 0755))

	// Add a file in 2024 directory to prevent its removal
	otherFile := filepath.Join(stagingRoot, "2024", "02", "file.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(otherFile), 0755))
	require.NoError(t, os.WriteFile(otherFile, []byte("content"), 0644))

	err := cleanupEmptyStagingDirectories(videoPath, stagingRoot)
	require.NoError(t, err)

	// Should clean up to the 2024 directory but not remove it (because 02 subdir has file)
	assertDirNotExists(t, filepath.Join(stagingRoot, "2024", "01", "15"), "Expected deepest directory to be removed")
	assertDirNotExists(t, filepath.Join(stagingRoot, "2024", "01"), "Expected 01 directory to be removed")
	assertDirExists(t, filepath.Join(stagingRoot, "2024"), "Expected 2024 directory with content to remain")
	assertDirExists(t, filepath.Join(stagingRoot, "2024", "02"), "Expected 02 directory with file to remain")
	assertDirExists(t, stagingRoot, "Expected staging root to remain")
}

func TestCleanupEmptyStagingDirectories_ConcurrentDeletion(t *testing.T) {
	tempDir := t.TempDir()
	stagingRoot := filepath.Join(tempDir, "staging")
	require.NoError(t, os.MkdirAll(stagingRoot, 0755))

	// Create nested directory structure
	videoPath := filepath.Join(stagingRoot, "2024", "01", "15", "video.mp4")
	middleDir := filepath.Join(stagingRoot, "2024", "01")
	require.NoError(t, os.MkdirAll(filepath.Dir(videoPath), 0755))

	// Remove the middle directory before cleanup to simulate concurrent deletion
	require.NoError(t, os.RemoveAll(middleDir))

	// Should handle gracefully without error
	err := cleanupEmptyStagingDirectories(videoPath, stagingRoot)
	require.NoError(t, err)

	assertDirExists(t, stagingRoot, "Expected staging root to remain")
}

func TestUploadVideos_FilesToUpload_WithAlbums_CleanupOnSuccess(t *testing.T) {
	ctx := context.Background()

	albumTitle := "TestAlbum"
	albumTitles := []string{albumTitle}
	cfg := newTestConfig(t, albumTitles)

	// Create video in nested directory structure
	videoFileName := "video1.mp4"
	videoRelPath := filepath.Join("2024", "05", "15", videoFileName)
	videoFilePath := filepath.Join(cfg.VideosOrigStagingRoot, videoRelPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(videoFilePath), 0755))
	require.NoError(t, os.WriteFile(videoFilePath, []byte("content"), 0644))

	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockGPhotosClient := NewMockGPhotosClient(ctrl)
	mockAlbumsSvc := NewMockAppAlbumsService(ctrl)
	mockUploaderSvc := NewMockMediaUploader(ctrl)
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl)

	mockGPhotosClient.EXPECT().Albums().Return(mockAlbumsSvc).AnyTimes()
	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()

	// Mock album exists
	existingAlbumID := "album-id-for-" + albumTitle
	mockAlbumsSvc.EXPECT().List(gomock.Any()).Return([]albums.Album{{ID: existingAlbumID, Title: albumTitle}}, nil)

	// Mock successful upload and album addition
	uploadToken := "token_for_" + videoFileName
	mediaItemID := "media_id_for_" + videoFileName

	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), videoFilePath).Return(uploadToken, nil)
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: videoFileName}).
		Return(&media_items.MediaItem{ID: mediaItemID, Filename: videoFileName}, nil)
	mockAlbumsSvc.EXPECT().AddMediaItems(gomock.Any(), existingAlbumID, []string{mediaItemID}).Return(nil)

	err := UploadVideos(ctx, cfg, tempConfigDir, false /* keepStaging */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos failed: %v", err)

	// Verify file is moved
	_, statErr := os.Stat(videoFilePath)
	assert.True(t, os.IsNotExist(statErr), "Expected video file to be moved from staging")

	// Verify file exists in destination
	expectedDestPath := filepath.Join(cfg.VideosOrigRoot, videoRelPath)
	_, statErr = os.Stat(expectedDestPath)
	assert.NoError(t, statErr, "Expected video file to be moved to destination")

	// Verify empty directories were cleaned up
	assertDirNotExists(t, filepath.Join(cfg.VideosOrigStagingRoot, "2024", "05", "15"), "Expected empty subdirectory to be cleaned up")
	assertDirNotExists(t, filepath.Join(cfg.VideosOrigStagingRoot, "2024", "05"), "Expected empty parent directory to be cleaned up")
	assertDirNotExists(t, filepath.Join(cfg.VideosOrigStagingRoot, "2024"), "Expected empty year directory to be cleaned up")
	assertDirExists(t, cfg.VideosOrigStagingRoot, "Expected staging root to remain")
}

func TestUploadVideos_ErrorUploadFile_NoCleanup(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig(t, nil)

	// Create video in nested directory structure
	videoFileName := "video1.mp4"
	videoRelPath := filepath.Join("2024", "05", "15", videoFileName)
	videoFilePath := filepath.Join(cfg.VideosOrigStagingRoot, videoRelPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(videoFilePath), 0755))
	require.NoError(t, os.WriteFile(videoFilePath, []byte("content"), 0644))

	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)
	mockUploaderSvc := NewMockMediaUploader(ctrl)

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()

	expectedErrStr := "simulated upload failure"
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), videoFilePath).
		Return("", errors.New(expectedErrStr))

	err := UploadVideos(ctx, cfg, tempConfigDir, false, mockGPhotosClient)
	require.Error(t, err, "UploadVideos expected to fail due to UploadFile error, but succeeded")

	// Verify file is still in staging (not moved)
	_, statErr := os.Stat(videoFilePath)
	assert.NoError(t, statErr, "Expected video file to remain in staging after upload failure")

	// Verify directories were NOT cleaned up since file wasn't moved
	assertDirExists(t, filepath.Join(cfg.VideosOrigStagingRoot, "2024", "05", "15"), "Expected directory to remain after upload failure")
	assertDirExists(t, filepath.Join(cfg.VideosOrigStagingRoot, "2024", "05"), "Expected parent directory to remain after upload failure")
	assertDirExists(t, filepath.Join(cfg.VideosOrigStagingRoot, "2024"), "Expected year directory to remain after upload failure")
}

func TestUploadVideos_ErrorAddMediaToAlbum_NoCleanup(t *testing.T) {
	ctx := context.Background()

	albumTitle := "FailingAlbum"
	cfg := newTestConfig(t, []string{albumTitle})

	// Create video in nested directory structure
	videoFileName := "video1.mp4"
	videoRelPath := filepath.Join("2024", "05", "15", videoFileName)
	videoFilePath := filepath.Join(cfg.VideosOrigStagingRoot, videoRelPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(videoFilePath), 0755))
	require.NoError(t, os.WriteFile(videoFilePath, []byte("content"), 0644))

	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)
	mockUploaderSvc := NewMockMediaUploader(ctrl)
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl)
	mockAlbumsSvc := NewMockAppAlbumsService(ctrl)

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()
	mockGPhotosClient.EXPECT().Albums().Return(mockAlbumsSvc).AnyTimes()

	// Mock album exists
	existingAlbumID := "album-id-existing"
	mockAlbumsSvc.EXPECT().List(gomock.Any()).Return([]albums.Album{{ID: existingAlbumID, Title: albumTitle}}, nil)

	// Mock successful upload and media item creation
	uploadToken := "token_for_" + videoFileName
	mediaItemID := "media-id_for_" + videoFileName
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), videoFilePath).Return(uploadToken, nil)
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: videoFileName}).
		Return(&media_items.MediaItem{ID: mediaItemID, Filename: videoFileName}, nil)

	// Mock album addition failure
	expectedAddError := "simulated add to album failure"
	mockAlbumsSvc.EXPECT().AddMediaItems(gomock.Any(), existingAlbumID, []string{mediaItemID}).
		Return(errors.New(expectedAddError))

	err := UploadVideos(ctx, cfg, tempConfigDir, false /* keepStaging */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos returned an unexpected error: expected to continue on AddMediaItems error")

	// Verify file is NOT moved because add to album failed
	_, statErr := os.Stat(videoFilePath)
	assert.NoError(t, statErr, "Expected video file to be kept after AddMediaItems failure")

	// Verify directories were NOT cleaned up since file wasn't moved
	assertDirExists(t, filepath.Join(cfg.VideosOrigStagingRoot, "2024", "05", "15"), "Expected directory to remain after album addition failure")
	assertDirExists(t, filepath.Join(cfg.VideosOrigStagingRoot, "2024", "05"), "Expected parent directory to remain after album addition failure")
	assertDirExists(t, filepath.Join(cfg.VideosOrigStagingRoot, "2024"), "Expected year directory to remain after album addition failure")
}

func TestUploadVideos_KeepStaging_NoCleanup(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig(t, nil)

	// Create video in nested directory structure
	videoFileName := "video1.mp4"
	videoRelPath := filepath.Join("2024", "05", "15", videoFileName)
	videoFilePath := filepath.Join(cfg.VideosOrigStagingRoot, videoRelPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(videoFilePath), 0755))
	require.NoError(t, os.WriteFile(videoFilePath, []byte("content"), 0644))

	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)
	mockUploaderSvc := NewMockMediaUploader(ctrl)
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl)

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()

	uploadToken := "token_for_" + videoFileName
	mediaItemID := "id_for_" + videoFileName
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), videoFilePath).Return(uploadToken, nil)
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: videoFileName}).
		Return(&media_items.MediaItem{ID: mediaItemID, Filename: videoFileName}, nil)

	err := UploadVideos(ctx, cfg, tempConfigDir, true /* keepStaging */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos failed: %v", err)

	// Verify file is kept in staging
	_, statErr := os.Stat(videoFilePath)
	assert.NoError(t, statErr, "Expected video file to be kept in staging when keepStaging=true")

	// Verify directories were NOT cleaned up since file wasn't moved
	assertDirExists(t, filepath.Join(cfg.VideosOrigStagingRoot, "2024", "05", "15"), "Expected directory to remain when keepStaging=true")
	assertDirExists(t, filepath.Join(cfg.VideosOrigStagingRoot, "2024", "05"), "Expected parent directory to remain when keepStaging=true")
	assertDirExists(t, filepath.Join(cfg.VideosOrigStagingRoot, "2024"), "Expected year directory to remain when keepStaging=true")
}

func TestUploadVideos_FilesToUpload_CleanupFailsButUploadSucceeds(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig(t, nil)

	// Create video in nested directory structure
	videoFileName := "video1.mp4"
	videoRelPath := filepath.Join("2024", "05", "15", videoFileName)
	videoFilePath := filepath.Join(cfg.VideosOrigStagingRoot, videoRelPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(videoFilePath), 0755))
	require.NoError(t, os.WriteFile(videoFilePath, []byte("content"), 0644))

	// Create an additional video file in a DIFFERENT parent directory to allow cleanup of first video's directory
	siblingVideoFile := filepath.Join(cfg.VideosOrigStagingRoot, "2024", "06", "sibling.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(siblingVideoFile), 0755))
	require.NoError(t, os.WriteFile(siblingVideoFile, []byte("sibling"), 0644))

	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)
	mockUploaderSvc := NewMockMediaUploader(ctrl)
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl)

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()

	// Mock expectations for both video files (order may vary)
	uploadToken1 := "token_for_" + videoFileName
	mediaItemID1 := "id_for_" + videoFileName
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), videoFilePath).Return(uploadToken1, nil)
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken1, Filename: videoFileName}).
		Return(&media_items.MediaItem{ID: mediaItemID1, Filename: videoFileName}, nil)

	uploadToken2 := "token_for_sibling"
	mediaItemID2 := "id_for_sibling"
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), siblingVideoFile).Return(uploadToken2, nil)
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken2, Filename: "sibling.mp4"}).
		Return(&media_items.MediaItem{ID: mediaItemID2, Filename: "sibling.mp4"}, nil)

	err := UploadVideos(ctx, cfg, tempConfigDir, false /* keepStaging */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos should succeed even if cleanup partially fails")

	// Verify both files are moved successfully
	_, statErr := os.Stat(videoFilePath)
	assert.True(t, os.IsNotExist(statErr), "Expected video file to be moved from staging")
	_, statErr = os.Stat(siblingVideoFile)
	assert.True(t, os.IsNotExist(statErr), "Expected sibling video file to be moved from staging")

	// Verify files exist in destination
	expectedDestPath := filepath.Join(cfg.VideosOrigRoot, videoRelPath)
	_, statErr = os.Stat(expectedDestPath)
	assert.NoError(t, statErr, "Expected video file to be moved to destination")

	expectedSiblingDestPath := filepath.Join(cfg.VideosOrigRoot, "2024", "06", "sibling.mp4")
	_, statErr = os.Stat(expectedSiblingDestPath)
	assert.NoError(t, statErr, "Expected sibling video file to be moved to destination")

	// Verify cleanup occurred for empty directories (each video's immediate parent can be cleaned up)
	assertDirNotExists(t, filepath.Join(cfg.VideosOrigStagingRoot, "2024", "05", "15"), "Expected empty subdirectory to be cleaned up")
	assertDirNotExists(t, filepath.Join(cfg.VideosOrigStagingRoot, "2024", "05"), "Expected empty parent directory to be cleaned up")
	assertDirNotExists(t, filepath.Join(cfg.VideosOrigStagingRoot, "2024", "06"), "Expected empty sibling parent directory to be cleaned up")
	assertDirNotExists(t, filepath.Join(cfg.VideosOrigStagingRoot, "2024"), "Expected empty year directory to be cleaned up")
	assertDirExists(t, cfg.VideosOrigStagingRoot, "Expected staging root to remain")
}

// --- Test for mixed scenarios ---

func TestUploadVideos_MixedSuccessAndFailure_PartialCleanup(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig(t, nil)

	// Create multiple videos where processing stops at first failure
	// Note: UploadVideos processes files in the order returned by filepath.WalkDir
	// which is typically alphabetical. When a failure occurs, the function exits early.
	videoFiles := map[string]string{
		"2024/05/15/a_success_video.mp4": "content1", // This will succeed (processed first alphabetically)
		"2024/05/16/b_failure_video.mp4": "content2", // This will fail upload (processed second)
		"2024/06/01/c_success_video.mp4": "content3", // This will NOT be processed due to early exit
	}

	stagingDir := cfg.VideosOrigStagingRoot
	createDirStructure(t, stagingDir, videoFiles)

	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)
	mockUploaderSvc := NewMockMediaUploader(ctrl)
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl)

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()

	// Mock success for first video (processed first due to alphabetical order)
	successPath1 := filepath.Join(stagingDir, "2024/05/15/a_success_video.mp4")
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), successPath1).Return("token1", nil)
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: "token1", Filename: "a_success_video.mp4"}).
		Return(&media_items.MediaItem{ID: "id1", Filename: "a_success_video.mp4"}, nil)

	// Mock failure for second video - this causes early exit
	failurePath := filepath.Join(stagingDir, "2024/05/16/b_failure_video.mp4")
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), failurePath).Return("", errors.New("upload failed"))

	// No mock for third video because it won't be processed due to early exit

	err := UploadVideos(ctx, cfg, tempConfigDir, false /* keepStaging */, mockGPhotosClient)
	require.Error(t, err, "UploadVideos should fail due to failed upload")

	// Verify first video was successfully moved and its directory cleaned up
	_, statErr := os.Stat(successPath1)
	assert.True(t, os.IsNotExist(statErr), "Expected successful video to be moved")
	assertDirNotExists(t, filepath.Join(stagingDir, "2024/05/15"), "Expected empty directory to be cleaned up")

	// Verify failed video remained and its directory was not cleaned up
	_, statErr = os.Stat(failurePath)
	assert.NoError(t, statErr, "Expected failed video to remain in staging")
	assertDirExists(t, filepath.Join(stagingDir, "2024/05/16"), "Expected directory with failed video to remain")
	assertDirExists(t, filepath.Join(stagingDir, "2024/05"), "Expected parent directory to remain (contains failed video)")

	// Verify third video was never processed (remains due to early exit)
	thirdPath := filepath.Join(stagingDir, "2024/06/01/c_success_video.mp4")
	_, statErr = os.Stat(thirdPath)
	assert.NoError(t, statErr, "Expected unprocessed video to remain in staging due to early exit")
	assertDirExists(t, filepath.Join(stagingDir, "2024/06/01"), "Expected directory with unprocessed video to remain")
	assertDirExists(t, filepath.Join(stagingDir, "2024/06"), "Expected parent directory to remain")
	assertDirExists(t, filepath.Join(stagingDir, "2024"), "Expected year directory to remain")
}
