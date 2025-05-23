package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync" // For wg in context cancellation test
	"testing"
	"time"

	"github.com/ccfrost/camedia/camediaconfig"
	"github.com/golang/mock/gomock"
	"github.com/gphotosuploader/google-photos-api-client-go/v3/albums"      // For types like albums.Album
	"github.com/gphotosuploader/google-photos-api-client-go/v3/media_items" // For types like media_items.SimpleMediaItem
)

// --- Test Helper Functions ---

func newTestConfig(stagingRoot string, defaultAlbums []string) camediaconfig.CamediaConfig {
	return camediaconfig.CamediaConfig{
		VideosOrigStagingRoot: stagingRoot,
		GooglePhotos: camediaconfig.GooglePhotosConfig{
			DefaultAlbums: defaultAlbums,
		},
	}
}

func createTempDirWithFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "test_staging_")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	// t.Cleanup(func() { os.RemoveAll(dir) }) // t.TempDir does this if used as base

	for name, content := range files {
		filePath := filepath.Join(dir, name)
		err := os.WriteFile(filePath, []byte(content), 0644)
		if err != nil {
			t.Fatalf("Failed to write file %s: %v", filePath, err)
		}
	}
	return dir
}

// --- Test Functions ---

func TestUploadVideos_StagingDirNotConfigured(t *testing.T) {
	cfg := newTestConfig("", nil)
	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl) // Changed from localMocks.NewMockGPhotosClient

	err := UploadVideos(context.Background(), cfg, t.TempDir(), false, mockGPhotosClient)
	if err == nil {
		t.Errorf("Expected an error when staging dir is not configured, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "video staging directory (VideosOrigStagingRoot) not configured") {
		t.Errorf("Expected error message about staging dir not configured, got: %v", err)
	}
}

func TestUploadVideos_StagingDirDoesNotExist(t *testing.T) {
	baseTmpDir := t.TempDir()
	nonExistentDir := filepath.Join(baseTmpDir, "nonexistent_subdir")
	cfg := newTestConfig(nonExistentDir, nil)
	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl) // Changed from localMocks.NewMockGPhotosClient

	err := UploadVideos(context.Background(), cfg, t.TempDir(), false, mockGPhotosClient)
	if err != nil {
		t.Errorf("Expected no error when staging dir does not exist, got: %v", err)
	}
}

func TestUploadVideos_EmptyStagingDir(t *testing.T) {
	stagingDir := t.TempDir()
	cfg := newTestConfig(stagingDir, nil)
	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl) // Changed from localMocks.NewMockGPhotosClient

	err := UploadVideos(context.Background(), cfg, t.TempDir(), false, mockGPhotosClient)
	if err != nil {
		t.Errorf("Expected no error for empty staging dir, got: %v", err)
	}
}

func TestUploadVideos_FilesToUpload_NoAlbums_DeleteFiles(t *testing.T) {
	ctx := context.Background()
	filesToCreate := map[string]string{"video1.mp4": "content1", "video2.mov": "content2"}
	stagingDir := createTempDirWithFiles(t, filesToCreate)
	t.Cleanup(func() { os.RemoveAll(stagingDir) })
	cfg := newTestConfig(stagingDir, nil) // No default albums
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
	if err != nil {
		t.Fatalf("UploadVideos failed: %v", err)
	}

	files, _ := os.ReadDir(stagingDir)
	if len(files) != 0 {
		t.Errorf("Expected staging directory to be empty, but found %d files", len(files))
		for _, f := range files {
			t.Logf("Found file: %s", f.Name())
		}
	}
}

func TestUploadVideos_FilesToUpload_NoAlbums_KeepFiles(t *testing.T) {
	ctx := context.Background()
	videoFile := "video1.mp4"
	stagingDir := createTempDirWithFiles(t, map[string]string{videoFile: "content1"})
	t.Cleanup(func() { os.RemoveAll(stagingDir) })

	cfg := newTestConfig(stagingDir, nil)
	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)        // Changed from localMocks.NewMockGPhotosClient
	mockUploaderSvc := NewMockMediaUploader(ctrl)          // Changed from localMocks.NewMockMediaUploader
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl) // Changed from localMocks.NewMockAppMediaItemsService

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()

	uploadToken := "token_for_" + videoFile
	mediaItemID := "id_for_" + videoFile
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), filepath.Join(stagingDir, videoFile)).
		Return(uploadToken, nil)
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: videoFile}).
		Return(&media_items.MediaItem{ID: mediaItemID, Filename: videoFile}, nil)

	err := UploadVideos(ctx, cfg, tempConfigDir, true /* keepStaging */, mockGPhotosClient)
	if err != nil {
		t.Fatalf("UploadVideos failed: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(stagingDir, videoFile)); os.IsNotExist(statErr) {
		t.Errorf("Expected %s to be kept in staging, but it was deleted", videoFile)
	}
}

// TestUploadVideos_FilesToUpload_WithAlbums_CreatesAndAddsToAlbum tests uploading a video,
// creating a new album when it doesn't exist, adding the video to it, and deleting the local file.
func TestUploadVideos_FilesToUpload_WithAlbums_CreatesAndAddsToAlbum(t *testing.T) {
	ctx := context.Background()
	videoFileName := "video1.mp4"
	videoFilePath := filepath.Join(t.TempDir(), videoFileName) // Use a unique path for the video file itself within a base temp dir
	baseStagingDir := filepath.Dir(videoFilePath)

	// Create the single video file in its own unique staging dir to avoid interference
	if err := os.MkdirAll(baseStagingDir, 0755); err != nil {
		t.Fatalf("Failed to create base staging dir: %v", err)
	}
	if err := os.WriteFile(videoFilePath, []byte("content"), 0644); err != nil {
		t.Fatalf("Failed to write video file: %v", err)
	}

	albumTitle := "NewAlbumToCreate"
	albumTitles := []string{albumTitle}
	cfg := newTestConfig(baseStagingDir, albumTitles)
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

	err := UploadVideos(ctx, cfg, tempConfigDir, false /* keepStaging */, mockGPhotosClient)
	if err != nil {
		t.Fatalf("UploadVideos failed: %v", err)
	}

	// Verify file is deleted
	if _, statErr := os.Stat(videoFilePath); !os.IsNotExist(statErr) {
		t.Errorf("Expected video file %s to be deleted, but it still exists. Error: %v", videoFilePath, statErr)
	}
}

func TestUploadVideos_ErrorLoadAlbumCache(t *testing.T) {
	ctx := context.Background()
	stagingDir := createTempDirWithFiles(t, map[string]string{"video1.mp4": "content"})
	t.Cleanup(func() { os.RemoveAll(stagingDir) })
	cfg := newTestConfig(stagingDir, []string{"Album1"})

	tempConfigDir := t.TempDir()
	// Ensure the cache path logic in test matches the main code's getAlbumCachePath
	// Assuming getAlbumCachePath uses configDir directly if provided, or os.UserConfigDir() + "camedia"
	// The constant is albumCacheFileName = "google_photos_album_cache.json"
	// If configDir is tempConfigDir, then cache path is filepath.Join(tempConfigDir, "google_photos_album_cache.json")
	// The main code uses: filepath.Join(configDir, albumCacheFileName)
	albumCacheFilePath := filepath.Join(tempConfigDir, "google_photos_album_cache.json")

	os.WriteFile(albumCacheFilePath, []byte("this is not json"), 0644)

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl) // Changed from localMocks.NewMockGPhotosClient

	uploadErr := UploadVideos(ctx, cfg, tempConfigDir, false, mockGPhotosClient)
	if uploadErr == nil {
		t.Fatalf("UploadVideos expected to fail due to malformed album cache, but succeeded")
	}
	if !strings.Contains(uploadErr.Error(), "failed to load album cache") {
		t.Errorf("Expected error about loading album cache, got: %v", uploadErr)
	}
}

func TestUploadVideos_ErrorGetOrCreateAlbumIDs(t *testing.T) {
	ctx := context.Background()
	stagingDir := createTempDirWithFiles(t, map[string]string{"video1.mp4": "content"})
	t.Cleanup(func() { os.RemoveAll(stagingDir) })
	albumTitles := []string{"AlbumThatCausesError"}
	cfg := newTestConfig(stagingDir, albumTitles)
	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl) // Changed from localMocks.NewMockGPhotosClient
	mockAlbumsSvc := NewMockAppAlbumsService(ctrl)  // Changed from localMocks.NewMockAppAlbumsService

	mockGPhotosClient.EXPECT().Albums().Return(mockAlbumsSvc).AnyTimes()

	expectedErrStr := "simulated error listing albums"
	// List returns a slice directly, not an iterator.
	mockAlbumsSvc.EXPECT().List(gomock.Any()).Return(nil, errors.New(expectedErrStr))

	err := UploadVideos(ctx, cfg, tempConfigDir, false, mockGPhotosClient)
	if err == nil {
		t.Fatalf("UploadVideos expected to fail due to error in getOrFetchAndCreateAlbumIDs, but succeeded")
	}
	if !strings.Contains(err.Error(), expectedErrStr) {
		t.Errorf("Expected error '%s', got: %v", expectedErrStr, err)
	}
}

func TestUploadVideos_ErrorUploadFile(t *testing.T) {
	ctx := context.Background()
	videoFileName := "video1.mp4"
	stagingDir := createTempDirWithFiles(t, map[string]string{videoFileName: "content"})
	t.Cleanup(func() { os.RemoveAll(stagingDir) })
	cfg := newTestConfig(stagingDir, nil)
	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl) // Changed from localMocks.NewMockGPhotosClient
	mockUploaderSvc := NewMockMediaUploader(ctrl)   // Changed from localMocks.NewMockMediaUploader

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()

	expectedErrStr := "simulated upload failure"
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), filepath.Join(stagingDir, videoFileName)).
		Return("", errors.New(expectedErrStr))

	err := UploadVideos(ctx, cfg, tempConfigDir, false, mockGPhotosClient)
	if err == nil {
		t.Fatalf("UploadVideos expected to fail due to UploadFile error, but succeeded")
	}
	if !strings.Contains(err.Error(), "failed to upload file") || !strings.Contains(err.Error(), videoFileName) || !strings.Contains(err.Error(), expectedErrStr) {
		t.Errorf("Expected error about failing to upload file '%s' with underlying error '%s', got: %v", videoFileName, expectedErrStr, err)
	}

	if _, statErr := os.Stat(filepath.Join(stagingDir, videoFileName)); os.IsNotExist(statErr) {
		t.Errorf("Expected %s to be kept in staging after upload failure, but it was deleted", videoFileName)
	}
}

func TestUploadVideos_ErrorCreateMediaItem(t *testing.T) {
	ctx := context.Background()
	videoFileName := "video1.mp4"
	stagingDir := createTempDirWithFiles(t, map[string]string{videoFileName: "content"})
	t.Cleanup(func() { os.RemoveAll(stagingDir) })
	cfg := newTestConfig(stagingDir, nil)
	tempConfigDir := t.TempDir()

	ctrl := gomock.NewController(t)
	mockGPhotosClient := NewMockGPhotosClient(ctrl)        // Changed from localMocks.NewMockGPhotosClient
	mockUploaderSvc := NewMockMediaUploader(ctrl)          // Changed from localMocks.NewMockMediaUploader
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl) // Changed from localMocks.NewMockAppMediaItemsService

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()

	uploadToken := "upload_token_for_" + videoFileName
	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), filepath.Join(stagingDir, videoFileName)).
		Return(uploadToken, nil)

	expectedErrStr := "simulated create media item failure"
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: uploadToken, Filename: videoFileName}).
		Return(nil, errors.New(expectedErrStr))

	err := UploadVideos(ctx, cfg, tempConfigDir, false, mockGPhotosClient)
	if err != nil {
		t.Fatalf("UploadVideos failed unexpectedly: %v. Expected to continue on CreateMediaItem error.", err)
	}

	if _, statErr := os.Stat(filepath.Join(stagingDir, videoFileName)); os.IsNotExist(statErr) {
		t.Errorf("Expected %s to be kept in staging after CreateMediaItem failure, but it was deleted", videoFileName)
	}
}

func TestUploadVideos_ErrorAddMediaToAlbum_FileKept_WhenAlbumExists(t *testing.T) {
	ctx := context.Background()
	videoFileName := "video1.mp4"
	videoFilePath := filepath.Join(t.TempDir(), videoFileName)
	baseStagingDir := filepath.Dir(videoFilePath)

	if err := os.MkdirAll(baseStagingDir, 0755); err != nil {
		t.Fatalf("Failed to create base staging dir: %v", err)
	}
	if err := os.WriteFile(videoFilePath, []byte("content"), 0644); err != nil {
		t.Fatalf("Failed to write video file: %v", err)
	}

	albumTitle := "ExistingAlbum"
	cfg := newTestConfig(baseStagingDir, []string{albumTitle})
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
	if err != nil {
		// The main UploadVideos function currently continues on AddMediaItems error, so no top-level error is expected here.
		// It logs the error and proceeds. If this behavior changes, this check needs an update.
		t.Fatalf("UploadVideos returned an unexpected error: %v. Expected to continue on AddMediaItems error.", err)
	}

	// Verify file is NOT deleted because add to album failed
	if _, statErr := os.Stat(videoFilePath); os.IsNotExist(statErr) {
		t.Errorf("Expected video file %s to be kept after AddMediaItems failure, but it was deleted", videoFilePath)
	}
}

func TestUploadVideos_ContextCancellationDuringLimiterWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	videoFileName := "video1.mp4"
	stagingDir := createTempDirWithFiles(t, map[string]string{videoFileName: "content"})
	t.Cleanup(func() { os.RemoveAll(stagingDir) })

	cfg := newTestConfig(stagingDir, nil)
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

	if errUpload == nil {
		t.Fatalf("Expected an error due to context cancellation, got nil")
	}
	if !errors.Is(errUpload, context.Canceled) && !errors.Is(errUpload, context.DeadlineExceeded) {
		t.Errorf("Expected context.Canceled or context.DeadlineExceeded, got %v", errUpload)
	}

	if _, statErr := os.Stat(filepath.Join(stagingDir, videoFileName)); os.IsNotExist(statErr) {
		t.Errorf("Expected %s to be kept in staging after context cancellation, but it was deleted", videoFileName)
	}
}

/*
// TestUploadVideos_StagingDirUnreadableByWalkDir - Needs OS-specific permission setup.
func TestUploadVideos_StagingDirUnreadableByWalkDir(t *testing
*/
