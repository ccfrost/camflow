package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ccfrost/camflow/camflowconfig"
	"github.com/golang/mock/gomock"
	"github.com/gphotosuploader/google-photos-api-client-go/v3/albums"
	"github.com/gphotosuploader/google-photos-api-client-go/v3/media_items"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestImportAndUploadVideosIntegration tests the complete workflow from importing
// media files from an SD card to uploading videos to Google Photos.
func TestImportAndUploadVideosIntegration(t *testing.T) {
	ctx := context.Background()

	// Setup test directories and config using the helper
	defaultAlbums := []string{"Test Album 1", "Test Album 2"}
	config := newTestConfig(t, defaultAlbums) // Use helper from util_test.go
	sdCardRoot := t.TempDir()                 // Still need a separate SD card root
	configDir := t.TempDir()                  // For album cache, etc.

	// DCIM directory needs to be under sdCardRoot
	dcimDir := filepath.Join(sdCardRoot, "DCIM")
	require.NoError(t, os.MkdirAll(dcimDir, 0755))

	// The newTestConfig helper already creates PhotosToProcessRoot and VideosExportQueueRoot
	photosToProcessRoot := config.PhotosToProcessRoot
	videosExportQueueRoot := config.VideosExportQueueRoot
	// VideosExportedRoot is also created by newTestConfig, can be accessed via config.VideosExportedRoot if needed for verification

	// Define test files with specific modification times
	testTime1 := time.Date(2024, 5, 15, 10, 30, 0, 0, time.UTC)
	testTime2 := time.Date(2024, 5, 16, 14, 45, 0, 0, time.UTC)

	testFiles := []struct {
		relPath  string
		content  string
		modTime  time.Time
		fileType string // "photo" or "video"
	}{
		{
			relPath:  "100CANON/IMG_0001.JPG",
			content:  "jpeg_content_1",
			modTime:  testTime1,
			fileType: "photo",
		},
		{
			relPath:  "100CANON/IMG_0002.CR3",
			content:  "raw_content_2",
			modTime:  testTime1,
			fileType: "photo",
		},
		{
			relPath:  "100CANON/VID_0001.MP4",
			content:  "video_content_1",
			modTime:  testTime1,
			fileType: "video",
		},
		{
			relPath:  "101CANON/IMG_0003.JPG",
			content:  "jpeg_content_3",
			modTime:  testTime2,
			fileType: "photo",
		},
		{
			relPath:  "101CANON/VID_0002.MP4",
			content:  "video_content_2",
			modTime:  testTime2,
			fileType: "video",
		},
	}

	// Create test files on the mock SD card
	createdFiles := make(map[string]string) // relPath -> full path
	for _, tf := range testFiles {
		fullPath := filepath.Join(dcimDir, tf.relPath)
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0755))
		require.NoError(t, os.WriteFile(fullPath, []byte(tf.content), 0644))
		require.NoError(t, os.Chtimes(fullPath, tf.modTime, tf.modTime))
		createdFiles[tf.relPath] = fullPath
	}

	t.Run("Step1_ImportFiles", func(t *testing.T) {
		// Run the import command - pass the SD card root, not the DCIM dir
		importResult, err := Import(config, sdCardRoot, false, time.Now()) // keepSrc = false
		require.NoError(t, err, "Import command should succeed")

		// Verify import results
		assert.Len(t, importResult.Photos, 2, "Should have imported photos from 2 directories")
		assert.Len(t, importResult.Videos, 2, "Should have imported videos from 2 directories")

		// Verify files were moved to correct locations
		for _, tf := range testFiles {
			srcPath := createdFiles[tf.relPath]

			// Source file should be deleted (keepSrc = false)
			_, err := os.Stat(srcPath)
			assert.True(t, os.IsNotExist(err), "Source file %s should be deleted", tf.relPath)

			// Calculate expected destination path
			var targetRoot string
			if tf.fileType == "photo" {
				targetRoot = photosToProcessRoot
			} else {
				targetRoot = videosExportQueueRoot
			}

			year, month, day := tf.modTime.Date()
			dateSubDir := filepath.Join(targetRoot,
				fmt.Sprintf("%d", year),
				fmt.Sprintf("%02d", month),
				fmt.Sprintf("%02d", day))

			baseName := filepath.Base(tf.relPath)
			targetFileName := fmt.Sprintf("%d-%02d-%02d-%s", year, month, day, baseName)
			expectedPath := filepath.Join(dateSubDir, targetFileName)

			// Verify destination file exists and has correct content
			content, err := os.ReadFile(expectedPath)
			require.NoError(t, err, "Destination file %s should exist", expectedPath)
			assert.Equal(t, tf.content, string(content), "File content should match for %s", tf.relPath)

			// Verify modification time was preserved
			info, err := os.Stat(expectedPath)
			require.NoError(t, err)
			assert.True(t, tf.modTime.Truncate(time.Second).Equal(info.ModTime().Truncate(time.Second)),
				"ModTime should be preserved for %s", tf.relPath)
		}
	})

	t.Run("Step2_UploadVideos", func(t *testing.T) {
		// Setup Google Photos API mocks
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockGPhotosClient := NewMockGPhotosClient(ctrl)
		mockUploaderSvc := NewMockMediaUploader(ctrl)
		mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl)
		mockAlbumsSvc := NewMockAppAlbumsService(ctrl)

		// Setup mock expectations
		mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
		mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()
		mockGPhotosClient.EXPECT().Albums().Return(mockAlbumsSvc).AnyTimes()

		// Find video files in orig
		videoFiles := []string{}
		for _, tf := range testFiles {
			if tf.fileType == "video" {
				year, month, day := tf.modTime.Date()
				dateSubDir := filepath.Join(videosExportQueueRoot,
					fmt.Sprintf("%d", year),
					fmt.Sprintf("%02d", month),
					fmt.Sprintf("%02d", day))

				baseName := filepath.Base(tf.relPath)
				targetFileName := fmt.Sprintf("%d-%02d-%02d-%s", year, month, day, baseName)
				videoPath := filepath.Join(dateSubDir, targetFileName)
				videoFiles = append(videoFiles, videoPath)
			}
		}

		// Mock album lookup and creation
		existingAlbum := &albums.Album{
			ID:    "album_id_1",
			Title: defaultAlbums[0],
		}
		mockAlbumsSvc.EXPECT().List(gomock.Any()).Return([]albums.Album{*existingAlbum}, nil)
		mockAlbumsSvc.EXPECT().Create(gomock.Any(), defaultAlbums[1]).Return(&albums.Album{
			ID:    "album_id_2",
			Title: defaultAlbums[1],
		}, nil)

		// Mock video uploads
		for i, videoPath := range videoFiles {
			uploadToken := fmt.Sprintf("upload_token_%d", i+1)
			mediaItemID := fmt.Sprintf("media_item_id_%d", i+1)
			fileName := filepath.Base(videoPath)

			// Mock upload
			mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), videoPath).
				Return(uploadToken, nil)

			// Mock media item creation
			mockMediaItemsSvc.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{
				UploadToken: uploadToken,
				Filename:    fileName,
			}).Return(&media_items.MediaItem{
				ID:       mediaItemID,
				Filename: fileName,
			}, nil)

			// Mock adding to albums
			for _, albumID := range []string{"album_id_1", "album_id_2"} {
				mockAlbumsSvc.EXPECT().AddMediaItems(gomock.Any(), albumID, []string{mediaItemID}).
					Return(nil)
			}
		}

		// Run upload-videos command
		err := UploadVideos(ctx, config, configDir, false, mockGPhotosClient) // keepTargetRoot = false
		require.NoError(t, err, "UploadVideos command should succeed")

		// Verify videos were deleted from orig (keepTargetRoot = false)
		for _, videoPath := range videoFiles {
			_, err := os.Stat(videoPath)
			assert.True(t, os.IsNotExist(err), "Video file %s should be deleted from orig", videoPath)
		}

		// Verify photos are still in their original location
		for _, tf := range testFiles {
			if tf.fileType == "photo" {
				year, month, day := tf.modTime.Date()
				dateSubDir := filepath.Join(photosToProcessRoot,
					fmt.Sprintf("%d", year),
					fmt.Sprintf("%02d", month),
					fmt.Sprintf("%02d", day))

				baseName := filepath.Base(tf.relPath)
				targetFileName := fmt.Sprintf("%d-%02d-%02d-%s", year, month, day, baseName)
				photoPath := filepath.Join(dateSubDir, targetFileName)

				_, err := os.Stat(photoPath)
				assert.NoError(t, err, "Photo file %s should still exist", photoPath)
			}
		}
	})
}

// TestImportAndUploadVideosIntegration_ErrorScenarios tests error handling in the integration workflow
func TestImportAndUploadVideosIntegration_ErrorScenarios(t *testing.T) {
	ctx := context.Background()

	t.Run("ImportError_InsufficientSpace", func(t *testing.T) {
		// This test would require mocking disk space, which is complex
		// For now, we'll test a simpler error scenario
		t.Skip("Disk space mocking not implemented")
	})

	t.Run("UploadError_GooglePhotosAPIFailure", func(t *testing.T) {
		// Setup test directories with a video file
		config := newTestConfig(t, []string{"Test Album"}) // Use helper
		sdCardRoot := t.TempDir()
		configDir := t.TempDir() // For album cache

		// DCIM directory needs to be under sdCardRoot
		dcimDir := filepath.Join(sdCardRoot, "DCIM", "100CANON") // Specific path for the video file
		require.NoError(t, os.MkdirAll(dcimDir, 0755))

		// Config fields are now set by newTestConfig
		// photosToProcessRoot := config.PhotosToProcessRoot // Not directly used in this specific sub-test for video
		videosExportQueueRoot := config.VideosExportQueueRoot

		// Create a test video file
		testTime := time.Date(2024, 5, 15, 10, 30, 0, 0, time.UTC)
		videoPath := filepath.Join(dcimDir, "VID_0001.MP4") // Path within sdCardRoot/DCIM/100CANON
		// require.NoError(t, os.MkdirAll(filepath.Dir(videoPath), 0755)) // dcimDir creation covers this
		require.NoError(t, os.WriteFile(videoPath, []byte("video_content"), 0644))
		require.NoError(t, os.Chtimes(videoPath, testTime, testTime))

		// Import the video
		// The Import command needs all photo paths in config to be valid for its own validation,
		// even if we are only testing video upload failure. newTestConfig handles this.
		_, err := Import(config, sdCardRoot, false, time.Now())
		require.NoError(t, err)

		// Setup mocks for upload failure
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockGPhotosClient := NewMockGPhotosClient(ctrl)
		mockUploaderSvc := NewMockMediaUploader(ctrl)
		mockAlbumsSvc := NewMockAppAlbumsService(ctrl)
		mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl)

		mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
		mockGPhotosClient.EXPECT().Albums().Return(mockAlbumsSvc).AnyTimes()
		mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()

		// Mock album listing (return empty - albums will be created)
		mockAlbumsSvc.EXPECT().List(gomock.Any()).Return([]albums.Album{}, nil).AnyTimes()

		// Mock album creation for default album
		mockAlbumsSvc.EXPECT().Create(gomock.Any(), "Test Album").Return(&albums.Album{
			ID:    "test_album_id",
			Title: "Test Album",
		}, nil).AnyTimes()

		// Mock upload failure
		mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), gomock.Any()).
			Return("", assert.AnError)

		// Run upload-videos command (should fail)
		err = UploadVideos(ctx, config, configDir, false, mockGPhotosClient)
		assert.Error(t, err, "UploadVideos should fail when Google Photos API fails")

		// Verify video file is still in orig dir (not deleted due to upload failure)
		year, month, day := testTime.Date()
		expectedVideoPath := filepath.Join(videosExportQueueRoot,
			fmt.Sprintf("%d", year),
			fmt.Sprintf("%02d", month),
			fmt.Sprintf("%02d", day),
			fmt.Sprintf("%d-%02d-%02d-VID_0001.MP4", year, month, day))

		_, err = os.Stat(expectedVideoPath)
		assert.NoError(t, err, "Video file should still exist in orig dir after upload failure")
	})

	t.Run("ConfigValidationError", func(t *testing.T) {
		// Test with invalid configuration
		invalidConfig := camflowconfig.CamediaConfig{
			// Missing required paths. newTestConfig cannot be used here as it creates a valid config.
			// To test Validate() properly for missing paths, we manually create an incomplete config.
			GooglePhotos: camflowconfig.GooglePhotosConfig{ // Need this to avoid nil pointer if Validate() on it is called
				ClientId:     "test",
				ClientSecret: "test",
				RedirectURI:  "test",
			},
		}

		err := invalidConfig.Validate()
		assert.Error(t, err, "Invalid config should fail validation")
	})
}

// TestImportAndUploadVideosIntegration_KeepFlags tests the workflow with keep flags enabled
func TestImportAndUploadVideosIntegration_KeepFlags(t *testing.T) {
	ctx := context.Background()

	// Setup test directories and config using the helper
	config := newTestConfig(t, []string{"Test Album"}) // Use helper
	sdCardRoot := t.TempDir()
	configDir := t.TempDir() // For album cache

	// DCIM directory needs to be under sdCardRoot
	dcimDir := filepath.Join(sdCardRoot, "DCIM", "100CANON")
	require.NoError(t, os.MkdirAll(dcimDir, 0755))

	// Config fields are now set by newTestConfig
	// photosToProcessRoot := config.PhotosToProcessRoot // Not directly used here
	videosExportQueueRoot := config.VideosExportQueueRoot

	// Create a test video file
	testTime := time.Date(2024, 5, 15, 10, 30, 0, 0, time.UTC)
	videoPath := filepath.Join(dcimDir, "VID_0001.MP4") // Path within sdCardRoot/DCIM/100CANON
	// require.NoError(t, os.MkdirAll(filepath.Dir(videoPath), 0755)) // dcimDir creation covers this
	require.NoError(t, os.WriteFile(videoPath, []byte("video_content"), 0644))
	require.NoError(t, os.Chtimes(videoPath, testTime, testTime))

	// Import with keepSrc = true
	// The Import command needs all photo paths in config to be valid.
	_, err := Import(config, sdCardRoot, true, time.Now())
	require.NoError(t, err)

	// Verify source file still exists
	_, err = os.Stat(videoPath)
	assert.NoError(t, err, "Source video should still exist when keepSrc=true")

	// Setup mocks for successful upload
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockGPhotosClient := NewMockGPhotosClient(ctrl)
	mockUploaderSvc := NewMockMediaUploader(ctrl)
	mockMediaItemsSvc := NewMockAppMediaItemsService(ctrl)
	mockAlbumsSvc := NewMockAppAlbumsService(ctrl)

	mockGPhotosClient.EXPECT().Uploader().Return(mockUploaderSvc).AnyTimes()
	mockGPhotosClient.EXPECT().MediaItems().Return(mockMediaItemsSvc).AnyTimes()
	mockGPhotosClient.EXPECT().Albums().Return(mockAlbumsSvc).AnyTimes()

	// Mock successful upload
	album := &albums.Album{ID: "album_id", Title: "Test Album"}
	mockAlbumsSvc.EXPECT().List(gomock.Any()).Return([]albums.Album{*album}, nil)

	year, month, day := testTime.Date()
	expectedVideoPath := filepath.Join(videosExportQueueRoot,
		fmt.Sprintf("%d", year),
		fmt.Sprintf("%02d", month),
		fmt.Sprintf("%02d", day),
		fmt.Sprintf("%d-%02d-%02d-VID_0001.MP4", year, month, day))

	mockUploaderSvc.EXPECT().UploadFile(gomock.Any(), expectedVideoPath).
		Return("upload_token", nil)
	mockMediaItemsSvc.EXPECT().Create(gomock.Any(), gomock.Any()).
		Return(&media_items.MediaItem{ID: "media_id", Filename: "2024-05-15-VID_0001.MP4"}, nil)
	mockAlbumsSvc.EXPECT().AddMediaItems(gomock.Any(), "album_id", []string{"media_id"}).
		Return(nil)

	// Upload with keepTargetRoot = true
	err = UploadVideos(ctx, config, configDir, true, mockGPhotosClient)
	require.NoError(t, err)

	// Verify video still exists in orig
	_, err = os.Stat(expectedVideoPath)
	assert.NoError(t, err, "Video should still exist in orig when keepTargetRoot=true")
}
