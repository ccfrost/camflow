package lib

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

func TestUploadVideos_TargetRootDirNotConfigured(t *testing.T) {
	cfg := newTestConfig(t, "", "") // No default albums
	// Intentionally make VideosExportQueueRoot unconfigured for this test case
	cfg.VideosExportQueueRoot = ""

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl) // Changed from localMocks.NewMockGPhotosClient

	err := UploadVideos(context.Background(), cfg, t.TempDir(), false, mockGPhotosClient)
	require.Error(t, err, "Expected an error when exportQueue dir is not configured, got nil")
	assert.Contains(t, err.Error(), "missing videos field", "Expected error message about exportQueue dir not configured, got: %v", err)
}

func TestUploadVideos_TargetRootDirDoesNotExist(t *testing.T) {
	cfg := newTestConfig(t, "", "") // No default albums
	require.NoError(t, os.RemoveAll(cfg.VideosExportQueueRoot))
	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl) // Changed from localMocks.NewMockGPhotosClient

	err := UploadVideos(context.Background(), cfg, t.TempDir(), false, mockGPhotosClient)
	assert.NoError(t, err, "Expected no error when exportQueue dir does not exist, got: %v", err)
}

func TestUploadVideos_EmptyTargetRootDir(t *testing.T) {
	cfg := newTestConfig(t, "", "") // No default albums
	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl) // Changed from localMocks.NewMockGPhotosClient

	err := UploadVideos(context.Background(), cfg, t.TempDir(), false, mockGPhotosClient)
	assert.NoError(t, err, "Expected no error for empty exportQueue dir, got: %v", err)
}

func TestUploadVideos_FilesToUpload_NoAlbums_MoveFiles(t *testing.T) {
	ctx := context.Background()

	cfg := newTestConfig(t, "", "") // No default albums
	filesToCreate := map[string]string{"2024-01-28-video1.mp4": "content1", "2024-01-29-video2.mov": "content2"}
	exportQueueDir := createTestFiles(t, cfg.VideosExportQueueRoot, filesToCreate)

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
		filePath := filepath.Join(exportQueueDir, baseName)
		uploadToken := "upload_token_for_" + baseName
		mediaItemID := "media_item_id_for_" + baseName

		mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), filePath).
			Return(uploadToken, nil)
		mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: baseName}).
			Return(&media_items.MediaItem{ID: mediaItemID, Filename: baseName}, nil)
	}

	err := UploadVideos(ctx, cfg, tempConfigDir, false /* keepQueued */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos failed: %v", err)

	// Verify files are moved from exportQueue and exist in VideosExportedRoot
	for baseName := range filesToCreate {
		exportQueuePath := filepath.Join(exportQueueDir, baseName)
		// Files should be moved to date-based structure in exported directory
		year, month, day, err := parseDatePrefix(baseName)
		require.NoError(t, err)
		destPath := filepath.Join(cfg.VideosExportedRoot, year, month, day, baseName)

		_, statErr := os.Stat(exportQueuePath)
		assert.True(t, os.IsNotExist(statErr), "Expected file %s to be moved from exportQueue %s, but it still exists", baseName, exportQueueDir)

		_, statErr = os.Stat(destPath)
		assert.NoError(t, statErr, "Expected file %s to be moved to %s, but it's not there", baseName, destPath)
	}

	// Check that exportQueue dir is now empty (since files were in root, no subdirectories to clean)
	remainingFiles, _ := os.ReadDir(exportQueueDir)
	assert.Empty(t, remainingFiles, "Expected exportQueue directory to be empty after moves, but found %d files", len(remainingFiles))
}

func TestUploadVideos_FilesToUpload_NoAlbums_KeepFiles(t *testing.T) {
	ctx := context.Background()

	cfg := newTestConfig(t, "", "") // No default albums
	videoFile := "2024-01-28-video1.mp4"
	createTestFiles(t, cfg.VideosExportQueueRoot, map[string]string{videoFile: "content1"})
	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)        // Changed from localMocks.NewMockGPhotosClient
	mockUploaderSvc := NewMockMediaUploader(ctrl)          // Changed from localMocks.NewMockMediaUploader
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl) // Changed from localMocks.NewMockAppMediaItemsService

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()

	uploadToken := "token_for_" + videoFile
	mediaItemID := "id_for_" + videoFile
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), filepath.Join(cfg.VideosExportQueueRoot, videoFile)).
		Return(uploadToken, nil)
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: videoFile}).
		Return(&media_items.MediaItem{ID: mediaItemID, Filename: videoFile}, nil)

	err := UploadVideos(ctx, cfg, tempConfigDir, true /* keepQueued */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos failed: %v", err)

	_, statErr := os.Stat(filepath.Join(cfg.VideosExportQueueRoot, videoFile))
	assert.NoError(t, statErr, "Expected %s to be kept in exportQueue, but it was deleted (os.IsNotExist was true for stat error: %v)", videoFile, statErr)
}

// TestUploadVideos_FilesToUpload_WithAlbums_CreatesAndAddsToAlbum tests uploading a video,
// creating a new album when it doesn't exist, adding the video to it, and moving the local file.
func TestUploadVideos_FilesToUpload_WithAlbums_CreatesAndAddsToAlbum(t *testing.T) {
	ctx := context.Background()

	albumTitle := "NewAlbumToCreate"
	cfg := newTestConfig(t, "", albumTitle) // Video default album

	videoFileName := "2024-01-28-video1.mp4"
	videoFilePath := filepath.Join(cfg.VideosExportQueueRoot, videoFileName)
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

	err = UploadVideos(ctx, cfg, tempConfigDir, false /* keepQueued */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos failed: %v", err)

	// Verify file is moved from exportQueue
	_, statErr := os.Stat(videoFilePath)
	assert.True(t, os.IsNotExist(statErr), "Expected video file %s to be moved from exportQueue, but it still exists. Error: %v", videoFilePath, statErr)

	// Verify file exists in VideosExportedRoot (moved to date-based structure)
	year, month, day, err := parseDatePrefix(videoFileName)
	require.NoError(t, err)
	expectedDestPath := filepath.Join(cfg.VideosExportedRoot, year, month, day, videoFileName)
	_, statErr = os.Stat(expectedDestPath)
	assert.NoError(t, statErr, "Expected video file %s to be moved to %s, but it does not exist. Error: %v", videoFileName, expectedDestPath, statErr)
}

func TestUploadVideos_ErrorLoadAlbumCache(t *testing.T) {
	ctx := context.Background()

	cfg := newTestConfig(t, "", "Album1") // Video default album
	createTestFiles(t, cfg.VideosExportQueueRoot, map[string]string{"2024-01-28-video1.mp4": "content"})

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
	albumTitle := "AlbumThatCausesError"
	cfg := newTestConfig(t, "", albumTitle) // Video default album
	createTestFiles(t, cfg.VideosExportQueueRoot, map[string]string{"2024-01-28-video1.mp4": "content"})
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
	cfg := newTestConfig(t, "", "") // No default albums
	videoFileName := "2024-01-28-video1.mp4"
	createTestFiles(t, cfg.VideosExportQueueRoot, map[string]string{videoFileName: "content"})
	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl) // Changed from localMocks.NewMockGPhotosClient
	mockUploaderSvc := NewMockMediaUploader(ctrl)   // Changed from localMocks.NewMockMediaUploader

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()

	expectedErrStr := "simulated upload failure"
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), filepath.Join(cfg.VideosExportQueueRoot, videoFileName)).
		Return("", errors.New(expectedErrStr))

	err := UploadVideos(ctx, cfg, tempConfigDir, false, mockGPhotosClient)
	require.Error(t, err, "UploadVideos expected to fail due to UploadFile error, but succeeded")
	assert.Contains(t, err.Error(), "failed to upload file", "Error message mismatch")
	assert.Contains(t, err.Error(), videoFileName, "Error message should contain filename")
	assert.Contains(t, err.Error(), expectedErrStr, "Error message should contain original error")

	_, statErr := os.Stat(filepath.Join(cfg.VideosExportQueueRoot, videoFileName))
	assert.NoError(t, statErr, "Expected %s to be kept in exportQueue after upload failure, but it was deleted (os.IsNotExist was true for stat error: %v)", videoFileName, statErr)
}

func TestUploadVideos_ErrorCreateMediaItem(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig(t, "", "") // No default albums
	videoFileName := "2024-01-28-video1.mp4"
	createTestFiles(t, cfg.VideosExportQueueRoot, map[string]string{videoFileName: "content"})
	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)        // Changed from localMocks.NewMockGPhotosClient
	mockUploaderSvc := NewMockMediaUploader(ctrl)          // Changed from localMocks.NewMockMediaUploader
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl) // Changed from localMocks.NewMockAppMediaItemsService

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()

	uploadToken := "upload_token_for_" + videoFileName
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), filepath.Join(cfg.VideosExportQueueRoot, videoFileName)).
		Return(uploadToken, nil)

	expectedErrStr := "simulated create media item failure"
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: videoFileName}).
		Return(nil, errors.New(expectedErrStr))

	err := UploadVideos(ctx, cfg, tempConfigDir, false, mockGPhotosClient)
	// UploadVideos should now return an error when CreateMediaItem fails.
	require.Error(t, err, "Expected UploadVideos to fail due to CreateMediaItem error, but it succeeded")
	assert.Contains(t, err.Error(), expectedErrStr, "Error message should include the CreateMediaItem failure")

	_, statErr := os.Stat(filepath.Join(cfg.VideosExportQueueRoot, videoFileName))
	assert.NoError(t, statErr, "Expected %s to be kept in exportQueue after CreateMediaItem failure, but it was deleted (os.IsNotExist was true for stat error: %v)", videoFileName, statErr)
}

func TestUploadVideos_ErrorAddMediaToAlbum_FileKept_WhenAlbumExists(t *testing.T) {
	ctx := context.Background()

	albumTitle := "ExistingAlbum"
	cfg := newTestConfig(t, "", albumTitle) // Video default album

	videoFileName := "2024-01-28-video1.mp4"
	videoFilePath := filepath.Join(cfg.VideosExportQueueRoot, videoFileName)
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

	err := UploadVideos(ctx, cfg, tempConfigDir, false /* keepQueued */, mockGPhotosClient)
	require.Error(t, err, "UploadVideos should have returned an error")
	assert.Contains(t, err.Error(), expectedAddError, "Error message should contain the original error")

	// Verify file is NOT deleted because add to album failed
	_, statErr := os.Stat(videoFilePath)
	assert.NoError(t, statErr, "Expected video file %s to be kept after AddMediaItems failure, but it was deleted (os.IsNotExist was true for stat error: %v)", videoFilePath, statErr)
}

func TestUploadVideos_ContextCancellationDuringLimiterWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	cfg := newTestConfig(t, "", "") // No default albums

	videoFileName := "2024-01-28-video1.mp4"
	createTestFiles(t, cfg.VideosExportQueueRoot, map[string]string{videoFileName: "content"})

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

	_, statErr := os.Stat(filepath.Join(cfg.VideosExportQueueRoot, videoFileName))
	assert.NoError(t, statErr, "Expected %s to be kept in exportQueue after context cancellation, but it was deleted (os.IsNotExist was true for stat error: %v)", videoFileName, statErr)
}

// TestUploadVideos_FilesToUpload_WithAlbums_AlbumExists tests uploading a video,
// using an existing album, adding the video to it, and moving the local file.
func TestUploadVideos_FilesToUpload_WithAlbums_AlbumExists(t *testing.T) {
	ctx := context.Background()

	albumTitle := "MyExistingAlbum"
	cfg := newTestConfig(t, "", albumTitle) // Video default album

	videoFileName := "2024-01-28-existing_album_video.mp4"
	videoFilePath := filepath.Join(cfg.VideosExportQueueRoot, videoFileName)
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

	err = UploadVideos(ctx, cfg, tempConfigDir, false /* keepQueued */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos failed: %v", err)

	// Verify file is moved from exportQueue
	_, statErr := os.Stat(videoFilePath)
	assert.True(t, os.IsNotExist(statErr), "Expected video file %s to be moved from exportQueue, but it still exists. Error: %v", videoFilePath, statErr)

	// Verify file exists in VideosExportedRoot (moved to date-based structure)
	year, month, day, err := parseDatePrefix(videoFileName)
	require.NoError(t, err)
	expectedDestPath := filepath.Join(cfg.VideosExportedRoot, year, month, day, videoFileName)
	_, statErr = os.Stat(expectedDestPath)
	assert.NoError(t, statErr, "Expected video file %s to be moved to %s, but it does not exist. Error: %v", videoFileName, expectedDestPath, statErr)
}

// --- Updated Existing Tests to Account for Cleanup ---

func TestUploadVideos_FilesToUpload_NoAlbums_MoveFiles_WithCleanup(t *testing.T) {
	ctx := context.Background()

	cfg := newTestConfig(t, "", "") // No default albums

	// Create files directly in export queue root (flat structure)
	filesToCreate := map[string]string{
		"2024-01-15-video1.mp4": "content1",
		"2024-01-16-video2.mov": "content2",
		"2024-02-01-video3.mp4": "content3",
	}

	exportQueueDir := cfg.VideosExportQueueRoot
	createTestFiles(t, exportQueueDir, filesToCreate)

	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)
	mockUploaderSvc := NewMockMediaUploader(ctrl)
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl)

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()

	// Expectations for UploadFile and CreateMediaItem for each file
	for baseName := range filesToCreate {
		filePath := filepath.Join(exportQueueDir, baseName)
		uploadToken := "upload_token_for_" + baseName
		mediaItemID := "media_item_id_for_" + baseName

		mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), filePath).
			Return(uploadToken, nil)
		mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: baseName}).
			Return(&media_items.MediaItem{ID: mediaItemID, Filename: baseName}, nil)
	}

	err := UploadVideos(ctx, cfg, tempConfigDir, false /* keepQueued */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos failed: %v", err)

	// Verify files are moved from exportQueue and exist in VideosExportedRoot
	for baseName := range filesToCreate {
		exportQueuePath := filepath.Join(exportQueueDir, baseName)
		// Files should be moved to date-based structure in exported directory
		year, month, day, err := parseDatePrefix(baseName)
		require.NoError(t, err)
		destPath := filepath.Join(cfg.VideosExportedRoot, year, month, day, baseName)

		_, statErr := os.Stat(exportQueuePath)
		assert.True(t, os.IsNotExist(statErr), "Expected file %s to be moved from exportQueue", baseName)

		_, statErr = os.Stat(destPath)
		assert.NoError(t, statErr, "Expected file %s to be moved to %s", baseName, destPath)
	}

	// No directory cleanup needed since files are directly in export queue root
	// TargetRoot root should still exist
	assertDirExists(t, exportQueueDir, "Expected exportQueue root to remain")
}

func TestUploadVideos_FilesToUpload_WithAlbums_CleanupOnSuccess(t *testing.T) {
	ctx := context.Background()

	albumTitle := "TestAlbum"
	cfg := newTestConfig(t, "", albumTitle) // Video default album

	// Create video directly in export queue root (flat structure)
	videoFileName := "2024-01-28-video1.mp4"
	videoFilePath := filepath.Join(cfg.VideosExportQueueRoot, videoFileName)
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

	err := UploadVideos(ctx, cfg, tempConfigDir, false /* keepQueued */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos failed: %v", err)

	// Verify file is moved
	_, statErr := os.Stat(videoFilePath)
	assert.True(t, os.IsNotExist(statErr), "Expected video file to be moved from exportQueue")

	// Verify file exists in destination (moved to date-based structure)
	year, month, day, err := parseDatePrefix(videoFileName)
	require.NoError(t, err)
	expectedDestPath := filepath.Join(cfg.VideosExportedRoot, year, month, day, videoFileName)
	_, statErr = os.Stat(expectedDestPath)
	assert.NoError(t, statErr, "Expected video file to be moved to destination")

	// No directory cleanup needed since file was directly in export queue root
	assertDirExists(t, cfg.VideosExportQueueRoot, "Expected exportQueue root to remain")
}

func TestUploadVideos_ErrorUploadFile_NoCleanup(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig(t, "", "") // No default albums

	// Create video in nested directory structure
	videoFileName := "2024-01-28-video1.mp4"
	videoRelPath := filepath.Join("2024", "05", "15", videoFileName)
	videoFilePath := filepath.Join(cfg.VideosExportQueueRoot, videoRelPath)
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

	// Verify file is still in exportQueue (not moved)
	_, statErr := os.Stat(videoFilePath)
	assert.NoError(t, statErr, "Expected video file to remain in exportQueue after upload failure")

	// Verify directories were NOT cleaned up since file wasn't moved
	assertDirExists(t, filepath.Join(cfg.VideosExportQueueRoot, "2024", "05", "15"), "Expected directory to remain after upload failure")
	assertDirExists(t, filepath.Join(cfg.VideosExportQueueRoot, "2024", "05"), "Expected parent directory to remain after upload failure")
	assertDirExists(t, filepath.Join(cfg.VideosExportQueueRoot, "2024"), "Expected year directory to remain after upload failure")
}

func TestUploadVideos_ErrorAddMediaToAlbum_NoCleanup(t *testing.T) {
	ctx := context.Background()

	albumTitle := "FailingAlbum"
	cfg := newTestConfig(t, "", albumTitle) // Video default album

	// Create video in nested directory structure
	videoFileName := "2024-01-28-video1.mp4"
	videoRelPath := filepath.Join("2024", "05", "15", videoFileName)
	videoFilePath := filepath.Join(cfg.VideosExportQueueRoot, videoRelPath)
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

	err := UploadVideos(ctx, cfg, tempConfigDir, false /* keepQueued */, mockGPhotosClient)
	require.Error(t, err, "UploadVideos should have returned an error")
	assert.Contains(t, err.Error(), expectedAddError, "Error message should contain the original error")

	// Verify file is NOT moved because add to album failed
	_, statErr := os.Stat(videoFilePath)
	assert.NoError(t, statErr, "Expected video file %s to be kept after AddMediaItems failure, but it was deleted (os.IsNotExist was true for stat error: %v)", videoFilePath, statErr)
}

func TestUploadVideos_keepQueued_NoCleanup(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig(t, "", "") // No default albums

	// Create video in nested directory structure
	videoFileName := "2024-01-28-video1.mp4"
	videoRelPath := filepath.Join("2024", "05", "15", videoFileName)
	videoFilePath := filepath.Join(cfg.VideosExportQueueRoot, videoRelPath)
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

	err := UploadVideos(ctx, cfg, tempConfigDir, true /* keepQueued */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos failed: %v", err)

	// Verify file is kept in exportQueue
	_, statErr := os.Stat(videoFilePath)
	assert.NoError(t, statErr, "Expected video file to be kept in exportQueue when keepQueued=true")

	// Verify directories were NOT cleaned up since file wasn't moved
	assertDirExists(t, filepath.Join(cfg.VideosExportQueueRoot, "2024", "05", "15"), "Expected directory to remain when keepQueued=true")
	assertDirExists(t, filepath.Join(cfg.VideosExportQueueRoot, "2024", "05"), "Expected parent directory to remain when keepQueued=true")
	assertDirExists(t, filepath.Join(cfg.VideosExportQueueRoot, "2024"), "Expected year directory to remain when keepQueued=true")
}

func TestUploadVideos_FilesToUpload_CleanupFailsButUploadSucceeds(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig(t, "", "") // No default albums

	// Create videos directly in export queue root (flat structure)
	videoFileName := "2024-01-28-video1.mp4"
	videoFilePath := filepath.Join(cfg.VideosExportQueueRoot, videoFileName)
	require.NoError(t, os.WriteFile(videoFilePath, []byte("content"), 0644))

	// Create an additional video file
	siblingVideoFile := filepath.Join(cfg.VideosExportQueueRoot, "2024-06-01-sibling.mp4")
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
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken2, Filename: "2024-06-01-sibling.mp4"}).
		Return(&media_items.MediaItem{ID: mediaItemID2, Filename: "2024-06-01-sibling.mp4"}, nil)

	err := UploadVideos(ctx, cfg, tempConfigDir, false /* keepQueued */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos should succeed even if cleanup partially fails")

	// Verify both files are moved successfully
	_, statErr := os.Stat(videoFilePath)
	assert.True(t, os.IsNotExist(statErr), "Expected video file to be moved from exportQueue")
	_, statErr = os.Stat(siblingVideoFile)
	assert.True(t, os.IsNotExist(statErr), "Expected sibling video file to be moved from exportQueue")

	// Verify files exist in destination (moved to date-based structure)
	year1, month1, day1, err := parseDatePrefix(videoFileName)
	require.NoError(t, err)
	expectedDestPath := filepath.Join(cfg.VideosExportedRoot, year1, month1, day1, videoFileName)
	_, statErr = os.Stat(expectedDestPath)
	assert.NoError(t, statErr, "Expected video file to be moved to destination")

	year2, month2, day2, err := parseDatePrefix("2024-06-01-sibling.mp4")
	require.NoError(t, err)
	expectedSiblingDestPath := filepath.Join(cfg.VideosExportedRoot, year2, month2, day2, "2024-06-01-sibling.mp4")
	_, statErr = os.Stat(expectedSiblingDestPath)
	assert.NoError(t, statErr, "Expected sibling video file to be moved to destination")

	// No directory cleanup needed since files were directly in export queue root
	assertDirExists(t, cfg.VideosExportQueueRoot, "Expected exportQueue root to remain")
}

// --- Test for mixed scenarios ---

func TestUploadVideos_MixedSuccessAndFailure_PartialCleanup(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig(t, "", "") // No default albums

	// Create multiple videos where processing stops at first failure
	// Note: UploadVideos processes files in the order returned by filepath.WalkDir
	// which is typically alphabetical. When a failure occurs, the function exits early.
	videoFiles := map[string]string{
		"2024-05-15-a_success_video.mp4": "content1", // This will succeed (processed first alphabetically)
		"2024-05-16-b_failure_video.mp4": "content2", // This will fail upload (processed second)
		"2024-06-01-c_success_video.mp4": "content3", // This will NOT be processed due to early exit
	}

	exportQueueDir := cfg.VideosExportQueueRoot
	createTestFiles(t, exportQueueDir, videoFiles)

	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)
	mockUploaderSvc := NewMockMediaUploader(ctrl)
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl)

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()

	// Mock success for first video (processed first due to alphabetical order)
	successPath1 := filepath.Join(exportQueueDir, "2024-05-15-a_success_video.mp4")
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), successPath1).Return("token1", nil)
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: "token1", Filename: "2024-05-15-a_success_video.mp4"}).
		Return(&media_items.MediaItem{ID: "id1", Filename: "2024-05-15-a_success_video.mp4"}, nil)

	// Mock failure for second video - this causes early exit
	failurePath := filepath.Join(exportQueueDir, "2024-05-16-b_failure_video.mp4")
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), failurePath).Return("", errors.New("upload failed"))

	// No mock for third video because it won't be processed due to early exit

	err := UploadVideos(ctx, cfg, tempConfigDir, false /* keepQueued */, mockGPhotosClient)
	require.Error(t, err, "UploadVideos should fail due to failed upload")

	// Verify first video was successfully moved
	_, statErr := os.Stat(successPath1)
	assert.True(t, os.IsNotExist(statErr), "Expected successful video to be moved")

	// Verify failed video remained
	_, statErr = os.Stat(failurePath)
	assert.NoError(t, statErr, "Expected failed video to remain in exportQueue")

	// Verify third video was never processed (remains due to early exit)
	thirdPath := filepath.Join(exportQueueDir, "2024-06-01-c_success_video.mp4")
	_, statErr = os.Stat(thirdPath)
	assert.NoError(t, statErr, "Expected unprocessed video to remain in exportQueue due to early exit")

	// No directory cleanup concerns since files are directly in export queue root
}

// --- Cross-Filesystem Tests (using IsSameFileSystemForTests_ForceFalse) ---

func TestUploadVideos_CrossFilesystem_NoAlbums_CopyAndDelete(t *testing.T) {
	ctx := context.Background()

	cfg := newTestConfig(t, "", "") // No default albums
	videoFileName := "2024-01-28-video1.mp4"
	filesToCreate := map[string]string{videoFileName: "video_content_cross_fs"}

	exportQueueDir := cfg.VideosExportQueueRoot
	createTestFiles(t, exportQueueDir, filesToCreate)
	tempConfigDir := t.TempDir()

	// Force cross-filesystem behavior
	originalValue := IsSameFileSystemForTests_ForceFalse
	defer func() { IsSameFileSystemForTests_ForceFalse = originalValue }()
	IsSameFileSystemForTests_ForceFalse = true

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)
	mockUploaderSvc := NewMockMediaUploader(ctrl)
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl)

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()

	uploadToken := "token_for_" + videoFileName
	mediaItemID := "media_id_for_" + videoFileName
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), filepath.Join(exportQueueDir, videoFileName)).Return(uploadToken, nil)
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: videoFileName}).
		Return(&media_items.MediaItem{ID: mediaItemID, Filename: videoFileName}, nil)

	err := UploadVideos(ctx, cfg, tempConfigDir, false /* keepQueued */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos should work with cross-filesystem copy+delete")

	// Verify file is moved from exportQueue using copy+delete
	exportQueuePath := filepath.Join(exportQueueDir, videoFileName)
	// Files should be moved to date-based structure in exported directory
	year, month, day, err := parseDatePrefix(videoFileName)
	require.NoError(t, err)
	destPath := filepath.Join(cfg.VideosExportedRoot, year, month, day, videoFileName)

	_, statErr := os.Stat(exportQueuePath)
	assert.True(t, os.IsNotExist(statErr), "Expected file %s to be deleted from exportQueue after copy+delete", videoFileName)

	_, statErr = os.Stat(destPath)
	assert.NoError(t, statErr, "Expected file %s to be copied to %s", videoFileName, destPath)

	// Verify file content is preserved
	originalContent := filesToCreate[videoFileName]
	copiedContent, err := os.ReadFile(destPath)
	require.NoError(t, err, "Failed to read copied file")
	assert.Equal(t, originalContent, string(copiedContent), "File content should be preserved during copy+delete")
}

func TestUploadVideos_CrossFilesystem_WithAlbums_CopyAndDelete(t *testing.T) {
	ctx := context.Background()

	albumTitle := "Test Album Cross FS"
	cfg := newTestConfig(t, "", albumTitle)
	videoFileName := "2024-06-20-cross-fs-video.mp4"
	filesToCreate := map[string]string{videoFileName: "video_content_with_albums_cross_fs"}

	exportQueueDir := cfg.VideosExportQueueRoot
	createTestFiles(t, exportQueueDir, filesToCreate)
	tempConfigDir := t.TempDir()

	// Force cross-filesystem behavior
	originalValue := IsSameFileSystemForTests_ForceFalse
	defer func() { IsSameFileSystemForTests_ForceFalse = originalValue }()
	IsSameFileSystemForTests_ForceFalse = true

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)
	mockUploaderSvc := NewMockMediaUploader(ctrl)
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl)
	mockAlbumsSvc := NewMockAppAlbumsService(ctrl)

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()
	mockGPhotosClient.EXPECT().Albums().Return(mockAlbumsSvc).AnyTimes()

	// Mock album creation/retrieval
	existingAlbumID := "existing_album_id_cross_fs"
	mockAlbumsSvc.EXPECT().List(gomock.Any()).Return([]albums.Album{
		{ID: existingAlbumID, Title: albumTitle},
	}, nil)

	// Mock file upload
	videoFilePath := filepath.Join(exportQueueDir, videoFileName)
	uploadToken := "token_for_cross_fs_video"
	mediaItemID := "media_id_for_cross_fs_video"
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), videoFilePath).Return(uploadToken, nil)
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: videoFileName}).
		Return(&media_items.MediaItem{ID: mediaItemID, Filename: videoFileName}, nil)
	mockAlbumsSvc.EXPECT().AddMediaItems(gomock.Any(), existingAlbumID, []string{mediaItemID}).Return(nil)

	err := UploadVideos(ctx, cfg, tempConfigDir, false /* keepQueued */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos with albums should work with cross-filesystem copy+delete")

	// Verify file is moved using copy+delete
	_, statErr := os.Stat(videoFilePath)
	assert.True(t, os.IsNotExist(statErr), "Expected video file to be deleted from exportQueue after copy+delete")

	// Verify file exists in destination with correct structure (moved to date-based structure)
	year, month, day, err := parseDatePrefix(videoFileName)
	require.NoError(t, err)
	expectedDestPath := filepath.Join(cfg.VideosExportedRoot, year, month, day, videoFileName)
	_, statErr = os.Stat(expectedDestPath)
	assert.NoError(t, statErr, "Expected video file to be copied to destination")

	// Verify file content is preserved
	originalContent := filesToCreate[videoFileName]
	copiedContent, err := os.ReadFile(expectedDestPath)
	require.NoError(t, err, "Failed to read copied file")
	assert.Equal(t, originalContent, string(copiedContent), "File content should be preserved during copy+delete")

	// No directory cleanup needed since file was directly in export queue root
	assertDirExists(t, exportQueueDir, "Expected exportQueue root to remain after copy+delete")
}

func TestUploadVideos_CrossFilesystem_KeepFiles_CopyOnly(t *testing.T) {
	ctx := context.Background()

	cfg := newTestConfig(t, "", "") // No default albums
	videoFileName := "keep-video.mp4"
	filesToCreate := map[string]string{videoFileName: "video_content_keep_cross_fs"}

	exportQueueDir := cfg.VideosExportQueueRoot
	createTestFiles(t, exportQueueDir, filesToCreate)
	tempConfigDir := t.TempDir()

	// Force cross-filesystem behavior
	originalValue := IsSameFileSystemForTests_ForceFalse
	defer func() { IsSameFileSystemForTests_ForceFalse = originalValue }()
	IsSameFileSystemForTests_ForceFalse = true

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)
	mockUploaderSvc := NewMockMediaUploader(ctrl)
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl)

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()

	uploadToken := "token_for_keep_" + videoFileName
	mediaItemID := "media_id_for_keep_" + videoFileName
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), filepath.Join(exportQueueDir, videoFileName)).Return(uploadToken, nil)
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: videoFileName}).
		Return(&media_items.MediaItem{ID: mediaItemID, Filename: videoFileName}, nil)

	err := UploadVideos(ctx, cfg, tempConfigDir, true /* keepQueued */, mockGPhotosClient)
	require.NoError(t, err, "UploadVideos with keepQueued should work with cross-filesystem behavior")

	// With keepQueued=true, file should remain in exportQueue and NOT be moved/copied
	exportQueuePath := filepath.Join(exportQueueDir, videoFileName)
	_, statErr := os.Stat(exportQueuePath)
	assert.NoError(t, statErr, "Expected file %s to remain in exportQueue when keepQueued=true", videoFileName)

	// Verify file content is unchanged
	originalContent := filesToCreate[videoFileName]
	sourceContent, err := os.ReadFile(exportQueuePath)
	require.NoError(t, err, "Failed to read source file")
	assert.Equal(t, originalContent, string(sourceContent), "Source file should be unchanged when keepQueued=true")

	// Verify file is NOT copied to destination (keepQueued=true means no move/copy)
	destPath := filepath.Join(cfg.VideosExportedRoot, videoFileName)
	_, statErr = os.Stat(destPath)
	assert.True(t, os.IsNotExist(statErr), "File should NOT be copied to destination when keepQueued=true")
}
