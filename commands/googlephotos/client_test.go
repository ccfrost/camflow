package googlephotos

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockHandler struct {
	t              *testing.T
	uploadURL      string
	uploadedBytes  int64
	uploadToken    string
	mediaItemID    string
	expectedChunks []int64
	chunkIndex     int
}

func (h *mockHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/uploads":
		// Handle upload start or chunk upload
		switch r.Header.Get("X-Goog-Upload-Protocol") {
		case "resumable":
			// Start new upload
			h.uploadURL = fmt.Sprintf("/upload/%d", time.Now().UnixNano())
			h.uploadedBytes = 0
			w.Header().Set("X-Goog-Upload-URL", h.uploadURL)
			w.WriteHeader(http.StatusOK)

		case "raw":
			// Handle direct upload (not used in resumable mode)
			h.uploadToken = "mock-upload-token"
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(h.uploadToken))
		}

	case h.uploadURL:
		// Handle chunk upload
		switch r.Header.Get("X-Goog-Upload-Command") {
		case "query":
			// Return current upload progress
			w.Header().Set("X-Goog-Upload-Size-Received", fmt.Sprintf("%d", h.uploadedBytes))
			w.WriteHeader(http.StatusOK)

		case "upload":
			// Read and validate chunk
			chunk, err := io.ReadAll(r.Body)
			require.NoError(h.t, err)

			// Verify chunk size matches expected
			if h.chunkIndex < len(h.expectedChunks) {
				assert.Equal(h.t, h.expectedChunks[h.chunkIndex], int64(len(chunk)),
					"Chunk size mismatch at index %d", h.chunkIndex)
			}

			h.uploadedBytes += int64(len(chunk))
			h.chunkIndex++

			// Check if this is the final chunk
			if h.uploadedBytes >= h.expectedChunks[len(h.expectedChunks)-1] {
				h.uploadToken = "mock-upload-token"
				w.Header().Set("X-Goog-Upload-Status", "final")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(h.uploadToken))
			} else {
				w.Header().Set("X-Goog-Upload-Size-Received", fmt.Sprintf("%d", h.uploadedBytes))
				w.WriteHeader(http.StatusOK)
			}
		}

	case "/mediaItems:batchCreate":
		// Handle media item creation
		var req struct {
			NewMediaItems []struct {
				Description     string `json:"description"`
				SimpleMediaItem struct {
					UploadToken string `json:"uploadToken"`
				} `json:"simpleMediaItem"`
			} `json:"newMediaItems"`
		}
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(h.t, err)

		assert.Equal(h.t, h.uploadToken, req.NewMediaItems[0].SimpleMediaItem.UploadToken)

		h.mediaItemID = "mock-media-item-id"
		resp := struct {
			NewMediaItemResults []struct {
				MediaItem MediaItem `json:"mediaItem"`
				Status    struct {
					Message string `json:"message"`
				} `json:"status"`
			} `json:"newMediaItemResults"`
		}{
			NewMediaItemResults: []struct {
				MediaItem MediaItem `json:"mediaItem"`
				Status    struct {
					Message string `json:"message"`
				} `json:"status"`
			}{
				{
					MediaItem: MediaItem{
						ID:          h.mediaItemID,
						Description: req.NewMediaItems[0].Description,
						ProductURL:  "https://photos.google.com/mock-url",
					},
					Status: struct {
						Message string `json:"message"`
					}{
						Message: "OK",
					},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}
}

func createTestVideo(t *testing.T, size int64) string {
	tmpDir, err := os.MkdirTemp("", "camedia-test-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	filename := filepath.Join(tmpDir, "test.mp4")
	f, err := os.Create(filename)
	require.NoError(t, err)
	defer f.Close()

	// Write pattern data
	chunk := make([]byte, 1024)
	for i := range chunk {
		chunk[i] = byte(i % 256)
	}

	remaining := size
	for remaining > 0 {
		writeSize := int64(len(chunk))
		if remaining < writeSize {
			writeSize = remaining
		}
		_, err = f.Write(chunk[:writeSize])
		require.NoError(t, err)
		remaining -= writeSize
	}

	return filename
}

func TestUploadVideo(t *testing.T) {
	tests := []struct {
		name           string
		fileSize       int64
		expectedChunks []int64
		wantErr        bool
	}{
		{
			name:           "Single small chunk upload",
			fileSize:       5 * 1024 * 1024,
			expectedChunks: []int64{5 * 1024 * 1024},
			wantErr:        false,
		},
		{
			name:     "Multiple full chunks",
			fileSize: 25 * 1024 * 1024,
			expectedChunks: []int64{
				10 * 1024 * 1024,
				10 * 1024 * 1024,
				5 * 1024 * 1024,
			},
			wantErr: false,
		},
		{
			name:     "Empty file",
			fileSize: 0,
			wantErr:  true,
		},
		{
			name:     "Huge file",
			fileSize: 11 * 1024 * 1024 * 1024, // 11GB
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server with mock handler
			handler := &mockHandler{
				t:              t,
				expectedChunks: tt.expectedChunks,
			}
			server := httptest.NewServer(handler)
			defer server.Close()

			// Override photosBaseURL for testing
			origBaseURL := photosBaseURL
			photosBaseURL = server.URL
			defer func() { photosBaseURL = origBaseURL }()

			// Create test video file
			filename := createTestVideo(t, tt.fileSize)

			// Create test client
			client := &Client{
				httpClient: &http.Client{},
			}

			// Track progress
			var progress []UploadProgress
			progressCallback := func(p UploadProgress) {
				progress = append(progress, p)
			}

			// Perform upload
			mediaItem, err := client.UploadVideo(context.Background(), filename, progressCallback)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, handler.mediaItemID, mediaItem.ID)

			// Verify progress reporting
			if len(tt.expectedChunks) > 0 {
				// Should have at least initial progress + one per chunk
				assert.GreaterOrEqual(t, len(progress), len(tt.expectedChunks)+1)

				// Initial progress should be 0
				assert.Equal(t, int64(0), progress[0].BytesUploaded)

				// Final progress should show complete
				lastProgress := progress[len(progress)-1]
				assert.Equal(t, tt.fileSize, lastProgress.BytesUploaded)
				assert.Equal(t, tt.fileSize, lastProgress.TotalBytes)
				assert.Equal(t, float64(1), lastProgress.ChunkProgress)
			}

			// Verify upload state cleanup
			uploadInfo, err := client.loadUploadInfo(filename)
			assert.Error(t, err, "Upload info should be deleted after successful upload")
			assert.Nil(t, uploadInfo)
		})
	}
}

func TestUploadResume(t *testing.T) {
	// Create a mock server that simulates an interrupted upload
	handler := &mockHandler{
		t: t,
		expectedChunks: []int64{
			10 * 1024 * 1024, // First chunk succeeds
			10 * 1024 * 1024, // Second chunk fails
			10 * 1024 * 1024, // After resume, second chunk succeeds
			5 * 1024 * 1024,  // Final chunk
		},
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	// Override photosBaseURL for testing
	origBaseURL := photosBaseURL
	photosBaseURL = server.URL
	defer func() { photosBaseURL = origBaseURL }()

	// Create test video file (35MB)
	filename := createTestVideo(t, 35*1024*1024)

	// Create test client
	client := &Client{
		httpClient: &http.Client{},
	}

	// Start upload but simulate failure during second chunk
	ctx, cancel := context.WithCancel(context.Background())
	cancelAfterFirstChunk := func(p UploadProgress) {
		if p.BytesUploaded >= 10*1024*1024 {
			cancel()
		}
	}

	_, err := client.UploadVideo(ctx, filename, cancelAfterFirstChunk)
	assert.Error(t, err)

	// Verify partial upload state was saved
	uploadInfo, err := client.loadUploadInfo(filename)
	require.NoError(t, err)
	assert.Equal(t, int64(10*1024*1024), uploadInfo.BytesUploaded)

	// Resume upload
	mediaItem, err := client.UploadVideo(context.Background(), filename, nil)
	require.NoError(t, err)
	assert.Equal(t, handler.mediaItemID, mediaItem.ID)

	// Verify upload resumed from saved position
	assert.Equal(t, int64(35*1024*1024), handler.uploadedBytes)
}

func TestValidateVideoFile(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) string
		wantErr bool
		errType error
	}{
		{
			name: "Valid video file",
			setup: func(t *testing.T) string {
				return createTestVideo(t, 5*1024*1024)
			},
			wantErr: false,
		},
		{
			name: "File too large",
			setup: func(t *testing.T) string {
				return createTestVideo(t, 11*1024*1024*1024)
			},
			wantErr: true,
			errType: ErrInvalidFile,
		},
		{
			name: "Empty file",
			setup: func(t *testing.T) string {
				return createTestVideo(t, 0)
			},
			wantErr: true,
			errType: ErrInvalidFile,
		},
		{
			name: "Non-existent file",
			setup: func(t *testing.T) string {
				return "/nonexistent/file.mp4"
			},
			wantErr: true,
			errType: ErrInvalidFile,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filename := tt.setup(t)
			client := &Client{}
			err := client.validateVideoFile(filename)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errType != nil {
					assert.ErrorIs(t, err, tt.errType)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestUploadState(t *testing.T) {
	// Create test file
	filename := createTestVideo(t, 5*1024*1024)
	info := &UploadInfo{
		Filename:      filename,
		UploadURL:     "https://test-upload-url",
		BytesUploaded: 1024 * 1024,
		TotalBytes:    5 * 1024 * 1024,
	}

	client := &Client{}

	// Test save and load
	err := client.saveUploadInfo(info)
	require.NoError(t, err)

	loaded, err := client.loadUploadInfo(filename)
	require.NoError(t, err)
	assert.Equal(t, info.Filename, loaded.Filename)
	assert.Equal(t, info.UploadURL, loaded.UploadURL)
	assert.Equal(t, info.BytesUploaded, loaded.BytesUploaded)
	assert.Equal(t, info.TotalBytes, loaded.TotalBytes)

	// Test delete
	err = client.deleteUploadInfo(filename)
	require.NoError(t, err)

	_, err = client.loadUploadInfo(filename)
	assert.Error(t, err)
}
