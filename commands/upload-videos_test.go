package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ccfrost/camedia/camediaconfig"
	"github.com/gphotosuploader/google-photos-api-client-go/v3"
	"github.com/gphotosuploader/google-photos-api-client-go/v3/albums"
	"github.com/gphotosuploader/google-photos-api-client-go/v3/media_items"
	"github.com/gphotosuploader/google-photos-api-client-go/v3/uploader"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	// Import grpc status for mock error simulation if needed
	// "google.golang.org/genproto/googleapis/rpc/status"
	// "google.golang.org/grpc/codes"
)

// --- Mock Interfaces (Ensure they satisfy the real interfaces) ---

// Assert that mocks implement the interfaces (compile-time check)
var _ albums.Service = (*mockAlbumsService)(nil)
var _ media_items.Service = (*mockMediaItemsService)(nil)
var _ uploader.Uploader = (*mockUploaderService)(nil)
var _ gphotos.Client = (*mockGPhotosClient)(nil)

// --- Mock Implementations ---

// Mock implementation for a type satisfying gphotos.Client interface
type mockGPhotosClient struct {
	mockAlbums     *mockAlbumsService
	mockMediaItems *mockMediaItemsService
	mockUploader   *mockUploaderService
}

// Implement methods returning the interfaces
func (m *mockGPhotosClient) Albums(ctx context.Context) albums.Service          { return m.mockAlbums }
func (m *mockGPhotosClient) MediaItems(ctx context.Context) media_items.Service { return m.mockMediaItems }
func (m *mockGPhotosClient) Uploader(ctx context.Context) uploader.Uploader     { return m.mockUploader }
func (m *mockGPhotosClient) Authenticate(ctx context.Context) error             { return nil } // Dummy implementation

func newMockGPhotosClient() *mockGPhotosClient {
	mockAlbums := &mockAlbumsService{albums: make(map[string]*albums.Album), addFailures: make(map[string]error), addedItems: make(map[string][]string)}
	mockMediaItems := &mockMediaItemsService{createFailures: make(map[string]error), createdItems: make(map[string]*media_items.MediaItem), batchCreateFailures: make(map[string]error)}
	mockUploader := &mockUploaderService{uploadFailures: make(map[string]error), uploadedFiles: make(map[string]string)}

	return &mockGPhotosClient{
		mockAlbums:     mockAlbums,
		mockMediaItems: mockMediaItems,
		mockUploader:   mockUploader,
	}
}

// Mock implementation for albums.Service
type mockAlbumsService struct {
	mu          sync.Mutex
	albums      map[string]*albums.Album // Title -> Album
	listError   error
	addFailures map[string]error    // albumID -> error
	addedItems  map[string][]string // albumID -> mediaItemIDs
}

// Implement all methods required by albums.Service
func (m *mockAlbumsService) AddMediaItems(ctx context.Context, albumID string, mediaItemIDs []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, exists := m.addFailures[albumID]; exists {
		return err
	}
	if m.addedItems == nil {
		m.addedItems = make(map[string][]string)
	}
	m.addedItems[albumID] = append(m.addedItems[albumID], mediaItemIDs...)
	return nil
}

func (m *mockAlbumsService) Create(ctx context.Context, title string) (*albums.Album, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.albums[title]; exists {
		return nil, fmt.Errorf("album '%s' already exists", title)
	}
	newAlbum := &albums.Album{ID: fmt.Sprintf("mock-album-%s", title), Title: title}
	m.albums[title] = newAlbum
	return newAlbum, nil
}

func (m *mockAlbumsService) Get(ctx context.Context, albumID string) (*albums.Album, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, album := range m.albums {
		if album.ID == albumID {
			return album, nil
		}
	}
	return nil, fmt.Errorf("album with ID '%s' not found", albumID)
}

func (m *mockAlbumsService) GetByTitle(ctx context.Context, albumTitle string) (*albums.Album, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if album, exists := m.albums[albumTitle]; exists {
		return album, nil
	}
	return nil, fmt.Errorf("gphotos: album not found: %s", albumTitle)
}

func (m *mockAlbumsService) List(ctx context.Context) ([]*albums.Album, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listError != nil {
		return nil, m.listError
	}
	var albumList []*albums.Album
	for _, album := range m.albums {
		albumList = append(albumList, album)
	}
	return albumList, nil
}

func (m *mockAlbumsService) ListByPage(ctx context.Context, pageToken string, pageSize int) ([]*albums.Album, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listError != nil {
		return nil, "", m.listError
	}
	var albumList []*albums.Album
	for _, album := range m.albums {
		albumList = append(albumList, album)
	}
	return albumList, "", nil // No next page token in this mock
}

// Use concrete types from the library
func (m *mockAlbumsService) Share(ctx context.Context, albumID string, options albums.ShareOptions) (*albums.ShareInfo, error) {
	// Dummy implementation
	return &albums.ShareInfo{IsJoined: false, ShareableUrl: "", ShareToken: "", IsOwned: true}, nil
}

func (m *mockAlbumsService) Unshare(ctx context.Context, albumID string) error {
	// Dummy implementation
	return nil
}

// Mock implementation for media_items.Service
type mockMediaItemsService struct {
	mu                  sync.Mutex
	createdItems        map[string]*media_items.MediaItem // uploadToken -> MediaItem
	createFailures      map[string]error                  // uploadToken -> error
	batchCreateFailures map[string]error                  // albumID -> error (simplified)
}

// Implement all methods required by media_items.Service
func (m *mockMediaItemsService) Create(ctx context.Context, item media_items.SimpleMediaItem) (*media_items.MediaItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, exists := m.createFailures[item.UploadToken]; exists {
		return nil, err
	}
	mediaItem := &media_items.MediaItem{
		ID:         fmt.Sprintf("media-%s", item.UploadToken),
		Filename:   item.Filename,
		ProductURL: "http://dummy.url/" + item.Filename,
	}
	if m.createdItems == nil {
		m.createdItems = make(map[string]*media_items.MediaItem)
	}
	m.createdItems[item.UploadToken] = mediaItem
	return mediaItem, nil
}

// Use concrete type from the library
func (m *mockMediaItemsService) BatchCreate(ctx context.Context, items []media_items.SimpleMediaItem, albumID string) ([]media_items.NewMediaItemResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, exists := m.batchCreateFailures[albumID]; exists {
		return nil, err
	}

	results := make([]media_items.NewMediaItemResult, len(items))
	for i, item := range items {
		if createErr, failExists := m.createFailures[item.UploadToken]; failExists {
			results[i] = media_items.NewMediaItemResult{
				UploadToken: item.UploadToken,
				// Use the Status type defined within media_items
				Status: &media_items.Status{Message: createErr.Error()}, // Example dummy error status
			}
		} else {
			mediaItem := &media_items.MediaItem{
				ID:         fmt.Sprintf("media-%s-batch-%s", item.UploadToken, albumID),
				Filename:   item.Filename,
				ProductURL: "http://dummy.url/batch/" + item.Filename,
			}
			results[i] = media_items.NewMediaItemResult{
				UploadToken: item.UploadToken,
				MediaItem:   mediaItem,
				// Use the Status type defined within media_items
				Status: &media_items.Status{}, // Example dummy OK status
			}
			if m.createdItems == nil {
				m.createdItems = make(map[string]*media_items.MediaItem)
			}
			m.createdItems[item.UploadToken] = mediaItem
		}
	}
	return results, nil
}

func (m *mockMediaItemsService) Get(ctx context.Context, mediaItemID string) (*media_items.MediaItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, item := range m.createdItems {
		if item.ID == mediaItemID {
			return item, nil
		}
	}
	return nil, fmt.Errorf("media item '%s' not found", mediaItemID)
}

func (m *mockMediaItemsService) List(ctx context.Context) ([]*media_items.MediaItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var itemList []*media_items.MediaItem
	for _, item := range m.createdItems {
		itemList = append(itemList, item)
	}
	return itemList, nil
}

func (m *mockMediaItemsService) ListByAlbum(ctx context.Context, albumID string) ([]*media_items.MediaItem, error) {
	return m.List(ctx)
}

func (m *mockMediaItemsService) ListByAlbumPage(ctx context.Context, albumID string, pageToken string, pageSize int) ([]*media_items.MediaItem, string, error) {
	items, err := m.List(ctx)
	return items, "", err
}

func (m *mockMediaItemsService) ListPage(ctx context.Context, pageToken string, pageSize int) ([]*media_items.MediaItem, string, error) {
	items, err := m.List(ctx)
	return items, "", err
}

// Use concrete type from the library
func (m *mockMediaItemsService) Search(ctx context.Context, filter media_items.Filter) ([]*media_items.MediaItem, error) {
	return m.List(ctx)
}

// Use concrete type from the library
func (m *mockMediaItemsService) SearchPage(ctx context.Context, filter media_items.Filter, pageToken string, pageSize int) ([]*media_items.MediaItem, string, error) {
	items, err := m.List(ctx)
	return items, "", err
}

// Mock implementation for uploader.Uploader
type mockUploaderService struct {
	mu             sync.Mutex
	uploadedFiles  map[string]string // filePath -> uploadToken
	uploadFailures map[string]error  // filename -> error
}

// Implement methods required by uploader.Uploader
// Use concrete type from the library
func (m *mockUploaderService) Upload(ctx context.Context, item uploader.UploadItem) (uploadToken string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var filename string
	if pi, ok := item.(interface{ Path() string }); ok {
		filename = filepath.Base(pi.Path())
	} else {
		filename = fmt.Sprintf("unknown-item-%d", len(m.uploadedFiles)+1)
	}

	if failureErr, exists := m.uploadFailures[filename]; exists {
		return "", failureErr
	}

	token := fmt.Sprintf("token-%s", filename)
	if m.uploadedFiles == nil {
		m.uploadedFiles = make(map[string]string)
	}
	if pi, ok := item.(interface{ Path() string }); ok {
		m.uploadedFiles[pi.Path()] = token
	} else {
		m.uploadedFiles[filename] = token
	}

	return token, nil
}

// Implement UploadFile as used in upload-videos.go
func (m *mockUploaderService) UploadFile(ctx context.Context, filePath string) (uploadToken string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	filename := filepath.Base(filePath)

	if failureErr, exists := m.uploadFailures[filename]; exists {
		return "", failureErr
	}

	token := fmt.Sprintf("token-%s", filename)
	if m.uploadedFiles == nil {
		m.uploadedFiles = make(map[string]string)
	}
	m.uploadedFiles[filePath] = token

	return token, nil
}

// --- Test Setup Helper ---

var videoStagingDirFunc = func() (string, error) {
	dir, err := os.UserCacheDir()
	if (err != nil) {
		return os.TempDir(), nil
	}
	return filepath.Join(dir, "camedia_test_staging"), nil
}

func setupTestDirs(t *testing.T) (configDir, stagingDir string) {
	t.Helper()
	configDir = t.TempDir()
	stagingDir = t.TempDir()

	originalVideoStagingDirFunc := videoStagingDirFunc
	videoStagingDirFunc = func() (string, error) {
		return stagingDir, nil
	}
	t.Cleanup(func() {
		videoStagingDirFunc = originalVideoStagingDirFunc
	})

	return configDir, stagingDir
}

func createDummyVideo(t *testing.T, dir, filename string, size int64) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	content := make([]byte, size)
	err := os.WriteFile(path, content, 0644)
	require.NoError(t, err)
	return path
}

// --- Test Cases ---

// ... (Test cases remain largely the same, ensuring mockClient is passed correctly) ...

func TestUploadVideos_Success_SingleFile(t *testing.T) {
	ctx := context.Background()
	configDir, stagingDir := setupTestDirs(t)

	config := camediaconfig.CamediaConfig{
		DefaultAlbums: []string{"Test Album 1"},
	}
	dummyFilePath := createDummyVideo(t, stagingDir, "video1.MP4", 1024)

	mockClient := newMockGPhotosClient()
	mockAlbums := mockClient.mockAlbums
	mockAlbums.albums["Test Album 1"] = &albums.Album{ID: "album1", Title: "Test Album 1"}
	mockUploader := mockClient.mockUploader
	mockMediaItems := mockClient.mockMediaItems

	err := UploadVideos(ctx, config, configDir, false, mockClient) // Pass mockClient (which implements gphotos.Client)
	assert.NoError(t, err)

	assert.Contains(t, mockUploader.uploadedFiles, dummyFilePath)
	uploadToken := mockUploader.uploadedFiles[dummyFilePath]
	require.Contains(t, mockMediaItems.createdItems, uploadToken)
	createdItem := mockMediaItems.createdItems[uploadToken]
	assert.Equal(t, "video1.MP4", createdItem.Filename)
	require.Contains(t, mockAlbums.addedItems, "album1")
	assert.Contains(t, mockAlbums.addedItems["album1"], createdItem.ID)

	_, err = os.Stat(dummyFilePath)
	assert.True(t, os.IsNotExist(err), "Expected dummy file to be deleted")

	cachePath := filepath.Join(configDir, albumCacheFileName)
	_, err = os.Stat(cachePath)
	assert.NoError(t, err, "Expected album cache file to be created")
}

func TestUploadVideos_Success_MultipleFiles_MultipleAlbums(t *testing.T) {
	ctx := context.Background()
	configDir, stagingDir := setupTestDirs(t)

	config := camediaconfig.CamediaConfig{
		DefaultAlbums: []string{"Album A", "Album B"},
	}
	dummyFilePath1 := createDummyVideo(t, stagingDir, "vid_a.MOV", 2048)
	dummyFilePath2 := createDummyVideo(t, stagingDir, "vid_b.MP4", 1024)

	mockClient := newMockGPhotosClient()
	mockAlbums := mockClient.mockAlbums
	mockUploader := mockClient.mockUploader
	mockMediaItems := mockClient.mockMediaItems
	mockAlbums.albums["Album A"] = &albums.Album{ID: "albumA_id", Title: "Album A"}
	mockAlbums.albums["Album B"] = &albums.Album{ID: "albumB_id", Title: "Album B"}

	err := UploadVideos(ctx, config, configDir, false, mockClient)
	assert.NoError(t, err)

	assert.Contains(t, mockUploader.uploadedFiles, dummyFilePath1)
	assert.Contains(t, mockUploader.uploadedFiles, dummyFilePath2)
	assert.Len(t, mockMediaItems.createdItems, 2)

	uploadToken1 := mockUploader.uploadedFiles[dummyFilePath1]
	uploadToken2 := mockUploader.uploadedFiles[dummyFilePath2]
	mediaItemID1 := mockMediaItems.createdItems[uploadToken1].ID
	mediaItemID2 := mockMediaItems.createdItems[uploadToken2].ID

	assert.Contains(t, mockAlbums.addedItems["albumA_id"], mediaItemID1)
	assert.Contains(t, mockAlbums.addedItems["albumA_id"], mediaItemID2)
	assert.Contains(t, mockAlbums.addedItems["albumB_id"], mediaItemID1)
	assert.Contains(t, mockAlbums.addedItems["albumB_id"], mediaItemID2)

	_, err = os.Stat(dummyFilePath1)
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(dummyFilePath2)
	assert.True(t, os.IsNotExist(err))
}

func TestUploadVideos_KeepStaging(t *testing.T) {
	ctx := context.Background()
	configDir, stagingDir := setupTestDirs(t)

	config := camediaconfig.CamediaConfig{
		DefaultAlbums: []string{"KeepAlbum"},
	}
	dummyFilePath := createDummyVideo(t, stagingDir, "keep_me.MP4", 512)

	mockClient := newMockGPhotosClient()
	mockAlbums := mockClient.mockAlbums
	mockUploader := mockClient.mockUploader
	mockMediaItems := mockClient.mockMediaItems
	mockAlbums.albums["KeepAlbum"] = &albums.Album{ID: "keep1", Title: "KeepAlbum"}

	err := UploadVideos(ctx, config, configDir, true, mockClient)
	assert.NoError(t, err)

	assert.Contains(t, mockUploader.uploadedFiles, dummyFilePath)
	uploadToken := mockUploader.uploadedFiles[dummyFilePath]
	require.Contains(t, mockMediaItems.createdItems, uploadToken)
	createdItem := mockMediaItems.createdItems[uploadToken]
	assert.Contains(t, mockAlbums.addedItems["keep1"], createdItem.ID)

	_, err = os.Stat(dummyFilePath)
	assert.NoError(t, err, "Expected dummy file to NOT be deleted")
}

func TestUploadVideos_NoDefaultAlbums(t *testing.T) {
	ctx := context.Background()
	configDir, stagingDir := setupTestDirs(t)

	config := camediaconfig.CamediaConfig{
		DefaultAlbums: []string{}, // No albums
	}
	dummyFilePath := createDummyVideo(t, stagingDir, "no_album.MP4", 512)

	mockClient := newMockGPhotosClient()
	mockAlbums := mockClient.mockAlbums
	mockUploader := mockClient.mockUploader
	mockMediaItems := mockClient.mockMediaItems

	err := UploadVideos(ctx, config, configDir, false, mockClient)
	assert.NoError(t, err)

	assert.Contains(t, mockUploader.uploadedFiles, dummyFilePath)
	assert.Len(t, mockMediaItems.createdItems, 1)
	assert.Empty(t, mockAlbums.addedItems, "Expected no items added to any albums")

	_, err = os.Stat(dummyFilePath)
	assert.True(t, os.IsNotExist(err))
}

func TestUploadVideos_EmptyStagingDir(t *testing.T) {
	ctx := context.Background()
	configDir, _ := setupTestDirs(t)

	config := camediaconfig.CamediaConfig{}
	mockClient := newMockGPhotosClient()
	mockUploader := mockClient.mockUploader
	mockMediaItems := mockClient.mockMediaItems
	mockAlbums := mockClient.mockAlbums

	err := UploadVideos(ctx, config, configDir, false, mockClient)
	assert.NoError(t, err)

	assert.Empty(t, mockUploader.uploadedFiles)
	assert.Empty(t, mockMediaItems.createdItems)
	assert.Empty(t, mockAlbums.addedItems)
}

func TestUploadVideos_StagingDirNotExist(t *testing.T) {
	ctx := context.Background()
	configDir := t.TempDir()
	stagingDir := filepath.Join(t.TempDir(), "nonexistent")

	originalVideoStagingDirFunc := videoStagingDirFunc
	videoStagingDirFunc = func() (string, error) {
		return stagingDir, nil
	}
	t.Cleanup(func() {
		videoStagingDirFunc = originalVideoStagingDirFunc
	})

	config := camediaconfig.CamediaConfig{}
	mockClient := newMockGPhotosClient()
	mockUploader := mockClient.mockUploader

	err := UploadVideos(ctx, config, configDir, false, mockClient)
	assert.NoError(t, err)

	assert.Empty(t, mockUploader.uploadedFiles)
}

func TestUploadVideos_UploadFailure(t *testing.T) {
	ctx := context.Background()
	configDir, stagingDir := setupTestDirs(t)

	config := camediaconfig.CamediaConfig{}
	dummyFilePath := createDummyVideo(t, stagingDir, "fail_upload.MP4", 512)

	mockClient := newMockGPhotosClient()
	mockUploader := mockClient.mockUploader
	mockMediaItems := mockClient.mockMediaItems
	mockUploader.uploadFailures["fail_upload.MP4"] = errors.New("upload failed")

	err := UploadVideos(ctx, config, configDir, false, mockClient)
	assert.NoError(t, err)

	assert.Empty(t, mockUploader.uploadedFiles)
	assert.Empty(t, mockMediaItems.createdItems)

	_, err = os.Stat(dummyFilePath)
	assert.NoError(t, err, "Expected file to NOT be deleted on upload failure")
}

func TestUploadVideos_CreateMediaItemFailure(t *testing.T) {
	ctx := context.Background()
	configDir, stagingDir := setupTestDirs(t)

	config := camediaconfig.CamediaConfig{}
	dummyFilePath := createDummyVideo(t, stagingDir, "fail_create.MP4", 512)

	mockClient := newMockGPhotosClient()
	mockUploader := mockClient.mockUploader
	mockMediaItems := mockClient.mockMediaItems
	uploadToken := "token-fail_create.MP4"
	mockMediaItems.createFailures[uploadToken] = errors.New("create failed")

	err := UploadVideos(ctx, config, configDir, false, mockClient)
	assert.NoError(t, err)

	assert.Contains(t, mockUploader.uploadedFiles, dummyFilePath)
	assert.Empty(t, mockMediaItems.createdItems)

	_, err = os.Stat(dummyFilePath)
	assert.NoError(t, err, "Expected file to NOT be deleted on create failure")
}

func TestUploadVideos_AddAlbumFailure(t *testing.T) {
	ctx := context.Background()
	configDir, stagingDir := setupTestDirs(t)

	config := camediaconfig.CamediaConfig{
		DefaultAlbums: []string{"Good Album", "Bad Album"},
	}
	dummyFilePath := createDummyVideo(t, stagingDir, "fail_add.MP4", 512)

	mockClient := newMockGPhotosClient()
	mockAlbums := mockClient.mockAlbums
	mockUploader := mockClient.mockUploader
	mockMediaItems := mockClient.mockMediaItems
	mockAlbums.albums["Good Album"] = &albums.Album{ID: "good1", Title: "Good Album"}
	mockAlbums.albums["Bad Album"] = &albums.Album{ID: "bad1", Title: "Bad Album"}
	mockAlbums.addFailures["bad1"] = errors.New("failed to add")

	err := UploadVideos(ctx, config, configDir, false, mockClient)
	assert.NoError(t, err)

	assert.Contains(t, mockUploader.uploadedFiles, dummyFilePath)
	uploadToken := mockUploader.uploadedFiles[dummyFilePath]
	require.Contains(t, mockMediaItems.createdItems, uploadToken)
	createdItem := mockMediaItems.createdItems[uploadToken]

	assert.Contains(t, mockAlbums.addedItems["good1"], createdItem.ID)
	assert.NotContains(t, mockAlbums.addedItems, "bad1")

	_, err = os.Stat(dummyFilePath)
	assert.NoError(t, err, "Expected file to NOT be deleted on album add failure")
}

func TestUploadVideos_AlbumNotFound(t *testing.T) {
	ctx := context.Background()
	configDir, stagingDir := setupTestDirs(t)

	config := camediaconfig.CamediaConfig{
		DefaultAlbums: []string{"Existing Album", "Missing Album"},
	}
	_ = createDummyVideo(t, stagingDir, "album_missing.MP4", 512)

	mockClient := newMockGPhotosClient()
	mockAlbums := mockClient.mockAlbums
	mockUploader := mockClient.mockUploader
	mockMediaItems := mockClient.mockMediaItems
	mockAlbums.albums["Existing Album"] = &albums.Album{ID: "existing1", Title: "Existing Album"}

	err := UploadVideos(ctx, config, configDir, false, mockClient)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "albums were not found")
	assert.Contains(t, err.Error(), "Missing Album")

	assert.Empty(t, mockUploader.uploadedFiles)
	assert.Empty(t, mockMediaItems.createdItems)
}

func TestUploadVideos_AlbumCacheLoadFailure(t *testing.T) {
	ctx := context.Background()
	configDir, stagingDir := setupTestDirs(t)

	cachePath := filepath.Join(configDir, albumCacheFileName)
	err := os.WriteFile(cachePath, []byte("this is not json"), 0644)
	require.NoError(t, err)

	config := camediaconfig.CamediaConfig{
		DefaultAlbums: []string{"Album From API"},
	}
	dummyFilePath := createDummyVideo(t, stagingDir, "cache_fail.MP4", 512)

	mockClient := newMockGPhotosClient()
	mockAlbums := mockClient.mockAlbums
	mockUploader := mockClient.mockUploader
	mockMediaItems := mockClient.mockMediaItems
	mockAlbums.albums["Album From API"] = &albums.Album{ID: "api_album1", Title: "Album From API"}

	err = UploadVideos(ctx, config, configDir, false, mockClient)
	assert.NoError(t, err)

	assert.Contains(t, mockUploader.uploadedFiles, dummyFilePath)
	uploadToken := mockUploader.uploadedFiles[dummyFilePath]
	require.Contains(t, mockMediaItems.createdItems, uploadToken)
	createdItem := mockMediaItems.createdItems[uploadToken]
	assert.Contains(t, mockAlbums.addedItems["api_album1"], createdItem.ID)

	_, err = os.Stat(dummyFilePath)
	assert.True(t, os.IsNotExist(err))

	cacheData, err := os.ReadFile(cachePath)
	require.NoError(t, err)
	assert.True(t, json.Valid(cacheData), "Expected cache file to contain valid JSON after rebuild")
	assert.Contains(t, string(cacheData), "Album From API")
}

func TestUploadVideos_AlbumCacheSaveFailure(t *testing.T) {
	ctx := context.Background()
	configDir, stagingDir := setupTestDirs(t)

	cachePath := filepath.Join(configDir, albumCacheFileName)
	_, err := os.Create(cachePath)
	require.NoError(t, err)
	err = os.Chmod(configDir, 0555)
	if err != nil {
		t.Logf("Could not set config dir to read-only: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(configDir, 0755)
	})

	config := camediaconfig.CamediaConfig{
		DefaultAlbums: []string{"New Album To Cache"},
	}
	_ = createDummyVideo(t, stagingDir, "cache_save_fail.MP4", 512)

	mockClient := newMockGPhotosClient()
	mockAlbums := mockClient.mockAlbums
	mockUploader := mockClient.mockUploader
	mockMediaItems := mockClient.mockMediaItems
	mockAlbums.albums["New Album To Cache"] = &albums.Album{ID: "new_album1", Title: "New Album To Cache"}

	err = UploadVideos(ctx, config, configDir, false, mockClient)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "error saving updated album cache")

	assert.Empty(t, mockUploader.uploadedFiles)
	assert.Empty(t, mockMediaItems.createdItems)
}
