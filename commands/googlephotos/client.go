package googlephotos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ccfrost/camedia/camediaconfig"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	tokenFile        = "token.json"
	appendOnlyScope  = "https://www.googleapis.com/auth/photoslibrary.appendonly"
	readAppDataScope = "https://www.googleapis.com/auth/photoslibrary.readonly.appcreateddata"
	editAppDataScope = "https://www.googleapis.com/auth/photoslibrary.edit.appcreateddata"
	uploadChunkSize  = 10 * 1024 * 1024        // 10MB chunks
	maxVideoSize     = 10 * 1024 * 1024 * 1024 // 10GB (Google Photos limit)
	minVideoSize     = 1                       // 1 byte minimum
	uploadTimeout    = 30 * time.Second        // Timeout for each chunk upload
)

var (
	// Base URL for Google Photos API - made variable for testing
	photosBaseURL = "https://photoslibrary.googleapis.com/v1"

	// Error types for validation
	ErrInvalidFile      = errors.New("invalid video file")
	ErrUploadStateStale = errors.New("upload state is stale")
	ErrChunkMismatch    = errors.New("chunk size mismatch")
)

// Album represents a Google Photos album
type Album struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	ProductURL      string `json:"productUrl"`
	MediaItemsCount string `json:"mediaItemsCount"`
}

// MediaItem represents a Google Photos media item
type MediaItem struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	ProductURL  string `json:"productUrl"`
}

// UploadInfo stores the state of a resumable upload
type UploadInfo struct {
	Filename      string `json:"filename"`
	UploadURL     string `json:"uploadUrl"`
	BytesUploaded int64  `json:"bytesUploaded"`
	TotalBytes    int64  `json:"totalBytes"`
}

// UploadProgress tracks progress of an upload
type UploadProgress struct {
	Filename      string
	BytesUploaded int64
	TotalBytes    int64
	ChunkProgress float64
}

// ProgressCallback is called to report upload progress
type ProgressCallback func(UploadProgress)

// Client handles interaction with Google Photos API
type Client struct {
	httpClient *http.Client
}

// NewClient creates a new Google Photos API client using OAuth credentials
func NewClient(ctx context.Context, config camediaconfig.CamediaConfig) (*Client, error) {
	// Create OAuth config
	oauthConfig := &oauth2.Config{
		ClientID:     config.GooglePhotos.ClientId,
		ClientSecret: config.GooglePhotos.ClientSecret,
		RedirectURL:  config.GooglePhotos.RedirectURI,
		Scopes: []string{
			appendOnlyScope,  // For uploading new media and creating albums
			readAppDataScope, // For reading our app's media items and albums
			editAppDataScope, // For editing our app's media items and albums
		},
		Endpoint: google.Endpoint,
	}

	// Try to load cached token
	token, err := loadToken()
	if err != nil {
		// Get new token
		token, err = getTokenFromWeb(oauthConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to get token: %w", err)
		}
		if err := saveToken(token); err != nil {
			return nil, fmt.Errorf("failed to save token: %w", err)
		}
	}

	return &Client{
		httpClient: oauthConfig.Client(ctx, token),
	}, nil
}

// getTokenFromWeb gets an OAuth token from the web flow
func getTokenFromWeb(config *oauth2.Config) (*oauth2.Token, error) {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser then type the "+
		"authorization code: \n%v\n", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		return nil, fmt.Errorf("unable to read authorization code: %w", err)
	}

	token, err := config.Exchange(context.Background(), authCode)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve token from web: %w", err)
	}
	return token, nil
}

func getTokenPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "camedia", tokenFile), nil
}

func loadToken() (*oauth2.Token, error) {
	path, err := getTokenPath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	token := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(token)
	return token, err
}

func saveToken(token *oauth2.Token) error {
	path, err := getTokenPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("unable to cache oauth token: %w", err)
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(token)
}

// UploadVideo uploads a video file to Google Photos with resumable upload support
func (c *Client) UploadVideo(ctx context.Context, filename string, progressCb ProgressCallback) (*MediaItem, error) {
	// Validate video file first
	if err := c.ValidateVideoFile(filename); err != nil { // Renamed call
		return nil, fmt.Errorf("invalid video file: %w", err)
	}

	// Try to load existing upload state
	info, err := c.loadUploadInfo(filename)
	if err != nil {
		// Start new upload
		info, err = c.startUploadSession(ctx, filename)
		if err != nil {
			return nil, fmt.Errorf("failed to start upload session: %w", err)
		}
	}

	// Report initial progress
	if progressCb != nil {
		progressCb(UploadProgress{
			Filename:      filename,
			BytesUploaded: info.BytesUploaded,
			TotalBytes:    info.TotalBytes,
			ChunkProgress: 0,
		})
	}

	// Upload the file in chunks
	uploadToken, err := c.uploadFileChunks(ctx, info, progressCb)
	if err != nil {
		return nil, fmt.Errorf("failed to upload file chunks: %w", err)
	}

	// Delete upload info file since upload is complete
	if err := c.deleteUploadInfo(filename); err != nil {
		fmt.Printf("warning: failed to delete upload info: %v\n", err)
	}

	// Create the media item using the upload token
	return c.createMediaItem(ctx, filepath.Base(filename), uploadToken)
}

func (c *Client) startUploadSession(ctx context.Context, filename string) (*UploadInfo, error) {
	fileInfo, err := os.Stat(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		photosBaseURL+"/uploads",
		nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-Goog-Upload-Protocol", "resumable")
	req.Header.Set("X-Goog-Upload-Command", "start")
	req.Header.Set("X-Goog-Upload-Content-Type", "video/mp4")
	req.Header.Set("X-Goog-Upload-Raw-Size", strconv.FormatInt(fileInfo.Size(), 10))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to start upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to start upload, status %d: %s", resp.StatusCode, body)
	}

	uploadURL := resp.Header.Get("X-Goog-Upload-URL")
	if uploadURL == "" {
		return nil, fmt.Errorf("no upload URL in response")
	}

	info := &UploadInfo{
		Filename:      filename,
		UploadURL:     uploadURL,
		BytesUploaded: 0,
		TotalBytes:    fileInfo.Size(),
	}

	if err := c.saveUploadInfo(info); err != nil {
		return nil, fmt.Errorf("failed to save upload info: %w", err)
	}

	return info, nil
}

func (c *Client) uploadFileChunks(ctx context.Context, info *UploadInfo, progressCb ProgressCallback) (string, error) {
	f, err := os.Open(info.Filename)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	// Seek to the last uploaded position confirmed by server (or 0 if new upload)
	if _, err := f.Seek(info.BytesUploaded, 0); err != nil {
		return "", fmt.Errorf("failed to seek in file to offset %d: %w", info.BytesUploaded, err)
	}

	chunk := make([]byte, uploadChunkSize) // Reusable buffer

	fmt.Printf("[Client] Starting upload loop. Filename: %s, Initial BytesUploaded: %d, TotalBytes: %d\n", info.Filename, info.BytesUploaded, info.TotalBytes) // Log loop start

	for info.BytesUploaded < info.TotalBytes {
		fmt.Printf("[Client] Loop iteration. BytesUploaded: %d / %d\n", info.BytesUploaded, info.TotalBytes) // Log start of iteration
		// Determine chunk size for this iteration
		bytesToRead := uploadChunkSize
		remainingBytes := info.TotalBytes - info.BytesUploaded
		if int64(bytesToRead) > remainingBytes {
			bytesToRead = int(remainingBytes)
		}

		// Read the exact chunk size needed
		fmt.Printf("[Client] Reading chunk. Offset: %d, Max size: %d\n", info.BytesUploaded, bytesToRead) // Log read attempt
		n, err := io.ReadFull(f, chunk[:bytesToRead])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF { // Allow EOF/UnexpectedEOF if it matches bytes read
			fmt.Printf("[Client] ERROR reading chunk: %v\n", err) // Log read error
			return "", fmt.Errorf("failed to read chunk at offset %d: %w", info.BytesUploaded, err)
		}
		if n == 0 { // Should not happen if BytesUploaded < TotalBytes, but check anyway
			fmt.Printf("[Client] Read 0 bytes unexpectedly. Breaking loop.\n") // Log zero read
			break // Exit loop if read returns 0 bytes
		}
		fmt.Printf("[Client] Read %d bytes.\n", n) // Log bytes read

		// Calculate content range for the chunk being sent
		currentOffset := info.BytesUploaded
		rangeEnd := currentOffset + int64(n) - 1
		contentRange := fmt.Sprintf("bytes %d-%d/%d",
			currentOffset, rangeEnd, info.TotalBytes)
		fmt.Printf("[Client] Sending chunk. URL: %s, Offset: %d, Size: %d, Range: %s\n", info.UploadURL, currentOffset, n, contentRange) // Log chunk send details

		// Create request with the chunk data directly
		reqCtx, cancel := context.WithTimeout(ctx, uploadTimeout) // Add timeout per chunk
		req, err := http.NewRequestWithContext(reqCtx, "POST",
			info.UploadURL,
			bytes.NewReader(chunk[:n])) // Use bytes.NewReader for the chunk data
		if err != nil {
			cancel() // Clean up context
			// If context is cancelled, return the context error directly
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				fmt.Printf("[Client] ERROR creating request (context error): %v\n", err)
				return "", err
			}
			fmt.Printf("[Client] ERROR creating request: %v\n", err)
			return "", fmt.Errorf("failed to create chunk request at offset %d: %w", currentOffset, err)
		}

		req.Header.Set("X-Goog-Upload-Command", "upload")
		// X-Goog-Upload-Offset is optional if Content-Range is provided, but doesn't hurt
		req.Header.Set("X-Goog-Upload-Offset", strconv.FormatInt(currentOffset, 10))
		req.Header.Set("Content-Range", contentRange)
		// Content-Length is set automatically by the http client for bytes.Reader

		resp, err := c.httpClient.Do(req)
		cancel() // Clean up context after request is done or failed
		if err != nil {
			// If context is cancelled, return the context error directly
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				fmt.Printf("[Client] ERROR sending chunk (context error): %v\n", err)
				return "", err
			}
			// Consider retrying certain network errors here? For now, fail.
			fmt.Printf("[Client] ERROR sending chunk: %v\n", err)
			return "", fmt.Errorf("failed to upload chunk at offset %d: %w", currentOffset, err)
		}

		// --- Response Handling ---
		statusCode := resp.StatusCode
		respHeaders := resp.Header.Clone() // Clone headers before body is closed
		bodyBytes, readErr := io.ReadAll(resp.Body) // Read body regardless of status
		resp.Body.Close()                           // Close body immediately

		fmt.Printf("[Client] Received response. Status: %d, Headers: %+v\n", statusCode, respHeaders) // Log response details
		if readErr != nil {
			fmt.Printf("[Client] WARNING: Error reading response body: %v\n", readErr)
		}
		if len(bodyBytes) > 0 {
			fmt.Printf("[Client] Response Body: %s\n", string(bodyBytes))
		}

		if statusCode == 308 { // Intermediate chunk success (Resume Incomplete)
			fmt.Printf("[Client] Handling 308 response.\n")
			rangeHeader := respHeaders.Get("Range")
			if rangeHeader == "" {
				fmt.Printf("[Client] ERROR: upload chunk 308 missing Range header (offset %d). Aborting.\n", currentOffset)
				return "", fmt.Errorf("upload chunk returned 308 but missing Range header (offset %d)", currentOffset)
			}
			// Parse "bytes=0-end"
			parts := strings.SplitN(rangeHeader, "=", 2)
			rangeVal := ""
			if len(parts) == 2 && parts[0] == "bytes" {
				rangeVal = parts[1]
			} else {
				fmt.Printf("[Client] ERROR: Invalid Range header format %q. Aborting.\n", rangeHeader)
				return "", fmt.Errorf("upload chunk returned 308 with invalid Range header format: %q (offset %d)", rangeHeader, currentOffset)
			}
			rangeParts := strings.SplitN(rangeVal, "-", 2)
			serverBytesUploaded := int64(0)
			if len(rangeParts) == 2 {
				endByte, parseErr := strconv.ParseInt(rangeParts[1], 10, 64)
				if parseErr != nil {
					fmt.Printf("[Client] ERROR: Invalid Range end value %q. Aborting.\n", rangeHeader)
					return "", fmt.Errorf("upload chunk returned 308 with invalid Range end value: %q (offset %d)", rangeHeader, currentOffset)
				}
				serverBytesUploaded = endByte + 1 // Range end is inclusive
			} else {
				fmt.Printf("[Client] ERROR: Invalid Range value format %q. Aborting.\n", rangeHeader)
				return "", fmt.Errorf("upload chunk returned 308 with invalid Range value format: %q (offset %d)", rangeHeader, currentOffset)
			}
			fmt.Printf("[Client] Parsed Range header '%s'. Server confirmed bytes: %d\n", rangeHeader, serverBytesUploaded)

			// Update client state ONLY based on server confirmation
			if serverBytesUploaded <= info.BytesUploaded {
				fmt.Printf("[Client] ERROR: Server range %q indicates %d bytes, but client tracked %d. Aborting.\n", rangeHeader, serverBytesUploaded, info.BytesUploaded)
				return "", fmt.Errorf("upload chunk 308 range mismatch: server ack'd %d bytes, client expected > %d", serverBytesUploaded, info.BytesUploaded)
			}
			fmt.Printf("[Client] Updating BytesUploaded from %d to %d based on server Range.\n", info.BytesUploaded, serverBytesUploaded)
			info.BytesUploaded = serverBytesUploaded

			// Report progress
			if progressCb != nil {
				fmt.Printf("[Client] Reporting progress: %d / %d\n", info.BytesUploaded, info.TotalBytes)
				progressCb(UploadProgress{
					Filename:      info.Filename,
					BytesUploaded: info.BytesUploaded, // Use updated value
					TotalBytes:    info.TotalBytes,
					ChunkProgress: 1.0, // Indicate chunk processing attempt complete
				})
			}
			// Save progress
			fmt.Printf("[Client] Saving upload info state.\n")
			if err := c.saveUploadInfo(info); err != nil {
				fmt.Printf("[Client] WARNING: failed to save upload progress after 308: %v\n", err)
			}

			// Seek file pointer to the new confirmed offset
			fmt.Printf("[Client] Seeking file to new offset: %d\n", info.BytesUploaded)
			_, seekErr := f.Seek(info.BytesUploaded, 0)
			if seekErr != nil {
				fmt.Printf("[Client] ERROR seeking file after 308: %v\n", seekErr)
				return "", fmt.Errorf("failed to seek file to offset %d after 308 response: %w", info.BytesUploaded, seekErr)
			}

			fmt.Printf("[Client] Continuing loop after 308 handling.\n")
			continue

		} else if statusCode == http.StatusOK || statusCode == http.StatusCreated { // Final chunk success
			fmt.Printf("[Client] Handling final response %d.\n", statusCode)
			expectedTotal := currentOffset + int64(n)
			if expectedTotal != info.TotalBytes {
				fmt.Printf("[Client] WARNING: final chunk response %d received, but calculated bytes %d != total bytes %d. Trusting server.\n", statusCode, expectedTotal, info.TotalBytes)
			}
			info.BytesUploaded = info.TotalBytes

			if progressCb != nil {
				fmt.Printf("[Client] Reporting final progress: %d / %d\n", info.BytesUploaded, info.TotalBytes)
				progressCb(UploadProgress{
					Filename:      info.Filename,
					BytesUploaded: info.BytesUploaded,
					TotalBytes:    info.TotalBytes,
					ChunkProgress: 1.0,
				})
			}

			if readErr != nil {
				fmt.Printf("[Client] ERROR reading final response body: %v\n", readErr)
				return "", fmt.Errorf("failed to read body from final response (status %d): %w", statusCode, readErr)
			}
			uploadToken := string(bodyBytes)
			if uploadToken == "" {
				fmt.Printf("[Client] ERROR: Empty upload token in final response %d.\n", statusCode)
				return "", fmt.Errorf("received empty upload token in final response (status %d)", statusCode)
			}
			fmt.Printf("[Client] Upload successful. Token: %s\n", uploadToken)
			return uploadToken, nil

		} else {
			fmt.Printf("[Client] Handling error response %d.\n", statusCode)
			errorMsg := string(bodyBytes)
			if readErr != nil {
				errorMsg = fmt.Sprintf("(failed to read error body: %v)", readErr)
			}
			if statusCode == http.StatusNotFound {
				fmt.Printf("[Client] ERROR: Upload URL %s not found (404). Session expired?\n", info.UploadURL)
				return "", fmt.Errorf("upload failed: upload URL %s not found (session may have expired)", info.UploadURL)
			}
			fmt.Printf("[Client] ERROR: Chunk upload failed. Status: %d, Offset: %d, Msg: %s\n", statusCode, currentOffset, errorMsg)
			return "", fmt.Errorf("chunk upload failed with status %d (offset %d): %s", statusCode, currentOffset, errorMsg)
		}
	}

	fmt.Printf("[Client] Exited upload loop. BytesUploaded: %d, TotalBytes: %d\n", info.BytesUploaded, info.TotalBytes)

	if info.BytesUploaded >= info.TotalBytes {
		fmt.Printf("[Client] Loop exited normally (BytesUploaded >= TotalBytes). Querying status...\n")
		finalStatus, finalToken, queryErr := c.queryUploadStatus(ctx, info)
		if queryErr != nil {
			fmt.Printf("[Client] ERROR querying status after loop exit: %v\n", queryErr)
			return "", fmt.Errorf("upload seemingly complete but failed to query final status: %w", queryErr)
		}
		fmt.Printf("[Client] Query result: Status=%s, Token=%s\n", finalStatus, finalToken)
		if finalStatus == "final" {
			if finalToken != "" {
				fmt.Printf("[Client] Query returned final status and token. Success.\n")
				return finalToken, nil
			}
			fmt.Printf("[Client] ERROR: Query returned final status but no token.\n")
			return "", fmt.Errorf("upload complete (queried status 'final') but failed to retrieve upload token from query")
		}
		fmt.Printf("[Client] ERROR: Query returned non-final status %s after loop exit.\n", finalStatus)
		return "", fmt.Errorf("upload seemingly complete (sent all bytes) but final status query returned status '%s'", finalStatus)

	} else {
		fmt.Printf("[Client] Loop exited unexpectedly (BytesUploaded %d < TotalBytes %d). Erroring out.\n", info.BytesUploaded, info.TotalBytes)
		return "", fmt.Errorf("upload loop finished unexpectedly at offset %d before reaching total bytes %d", info.BytesUploaded, info.TotalBytes)
	}
}

func (c *Client) queryUploadStatus(ctx context.Context, info *UploadInfo) (string, string, error) {
	fmt.Printf("[Client] Querying upload status for URL: %s\n", info.UploadURL)
	req, err := http.NewRequestWithContext(ctx, "POST", info.UploadURL, nil)
	if err != nil {
		fmt.Printf("[Client] ERROR creating query request: %v\n", err)
		return "", "", fmt.Errorf("failed to create query request: %w", err)
	}
	req.Header.Set("X-Goog-Upload-Command", "query")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		fmt.Printf("[Client] ERROR executing query request: %v\n", err)
		return "", "", fmt.Errorf("failed to query upload status: %w", err)
	}
	defer resp.Body.Close()

	fmt.Printf("[Client] Query response status: %d, Headers: %+v\n", resp.StatusCode, resp.Header)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPermanentRedirect {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("[Client] ERROR: Query request failed. Status: %d, Body: %s\n", resp.StatusCode, string(body))
		return "", "", fmt.Errorf("query upload status failed with status %d: %s", resp.StatusCode, string(body))
	}

	status := resp.Header.Get("X-Goog-Upload-Status")
	token := ""
	if status == "final" {
		bodyBytes, _ := io.ReadAll(resp.Body)
		if len(bodyBytes) > 0 {
			fmt.Printf("[Client] WARNING: queryUploadStatus received status 'final' with non-empty body: %s\n", string(bodyBytes))
			token = string(bodyBytes)
		}
	}

	fmt.Printf("[Client] Query result: Status=%s, Token=%s\n", status, token)

	return status, token, nil
}

func (c *Client) createMediaItem(ctx context.Context, filename, uploadToken string) (*MediaItem, error) {
	reqBody := map[string]interface{}{
		"newMediaItems": []map[string]interface{}{
			{
				"description": filename,
				"simpleMediaItem": map[string]string{
					"uploadToken": uploadToken,
				},
			},
		},
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		photosBaseURL+"/mediaItems:batchCreate",
		bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to create media item: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to create media item: %s", body)
	}

	var result struct {
		NewMediaItemResults []struct {
			MediaItem MediaItem `json:"mediaItem"`
			Status    struct {
				Message string `json:"message"`
			} `json:"status"`
		} `json:"newMediaItemResults"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(result.NewMediaItemResults) == 0 {
		return nil, fmt.Errorf("no media items created")
	}

	if result.NewMediaItemResults[0].Status.Message != "OK" {
		return nil, fmt.Errorf("failed to create media item: %s",
			result.NewMediaItemResults[0].Status.Message)
	}

	return &result.NewMediaItemResults[0].MediaItem, nil
}

func getUploadInfoPath(filename string) (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	base := filepath.Base(filename)
	return filepath.Join(configDir, "camedia", "uploads", base+".upload"), nil
}

func (c *Client) loadUploadInfo(filename string) (*UploadInfo, error) {
	path, err := getUploadInfoPath(filename)
	if err != nil {
		fmt.Printf("[Client|loadUploadInfo] Error getting path for %s: %v\n", filename, err)
		return nil, err
	}
	fmt.Printf("[Client|loadUploadInfo] Attempting to load from: %s\n", path)

	f, err := os.Open(path)
	if err != nil {
		fmt.Printf("[Client|loadUploadInfo] Error opening %s: %v\n", path, err)
		return nil, err // Return the original error (e.g., os.ErrNotExist)
	}
	defer f.Close()

	var info UploadInfo
	if err := json.NewDecoder(f).Decode(&info); err != nil {
		fmt.Printf("[Client|loadUploadInfo] Error decoding %s: %v\n", path, err)
		return nil, err
	}

	// Verify the file still exists and has the same size
	fileInfo, err := os.Stat(filename)
	if err != nil {
		fmt.Printf("[Client|loadUploadInfo] Error stating original file %s: %v\n", filename, err)
		return nil, err
	}
	if fileInfo.Size() != info.TotalBytes {
		fmt.Printf("[Client|loadUploadInfo] File size mismatch for %s: expected %d, got %d\n", filename, info.TotalBytes, fileInfo.Size())
		return nil, fmt.Errorf("file size changed")
	}
	fmt.Printf("[Client|loadUploadInfo] Successfully loaded state for %s: %+v\n", filename, info)

	return &info, nil
}

func (c *Client) saveUploadInfo(info *UploadInfo) error {
	path, err := getUploadInfoPath(info.Filename)
	if err != nil {
		fmt.Printf("[Client|saveUploadInfo] Error getting path for %s: %v\n", info.Filename, err)
		return err
	}
	fmt.Printf("[Client|saveUploadInfo] Attempting to save state to: %s for %+v\n", path, info)

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		fmt.Printf("[Client|saveUploadInfo] Error creating directory for %s: %v\n", path, err)
		return err
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		fmt.Printf("[Client|saveUploadInfo] Error opening/creating %s: %v\n", path, err)
		return fmt.Errorf("failed to create upload info file: %w", err)
	}
	defer f.Close()

	err = json.NewEncoder(f).Encode(info)
	if err != nil {
		fmt.Printf("[Client|saveUploadInfo] Error encoding state to %s: %v\n", path, err)
		return err
	}
	fmt.Printf("[Client|saveUploadInfo] Successfully saved state to %s\n", path)
	return nil
}

func (c *Client) deleteUploadInfo(filename string) error {
	path, err := getUploadInfoPath(filename)
	if err != nil {
		fmt.Printf("[Client|deleteUploadInfo] Error getting path for %s: %v\n", filename, err)
		return err
	}
	fmt.Printf("[Client|deleteUploadInfo] Attempting to delete: %s\n", path)
	err = os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) { // Don't log error if file already doesn't exist
		fmt.Printf("[Client|deleteUploadInfo] Error deleting %s: %v\n", path, err)
	}
	return err
}

// GetAlbum gets an album by title. Returns an error if not found.
func (c *Client) GetAlbum(ctx context.Context, title string) (*Album, error) {
	// First try to find existing album
	var pageToken string
	for {
		url := photosBaseURL + "/albums?pageSize=50"
		if pageToken != "" {
			url += "&pageToken=" + pageToken
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to list albums: %w", err)
		}
		defer resp.Body.Close()

		// Check for non-OK status codes after potential redirects
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("failed to list albums, status %d: %s", resp.StatusCode, body)
		}

		var result struct {
			Albums        []Album `json:"albums"`
			NextPageToken string  `json:"nextPageToken"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			// Handle potential empty body on non-200 status if not caught above
			if err == io.EOF && resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("failed to list albums, status %d: empty response body", resp.StatusCode)
			}
			return nil, fmt.Errorf("failed to decode albums: %w", err)
		}

		for _, album := range result.Albums {
			if album.Title == title {
				return &album, nil
			}
		}

		if result.NextPageToken == "" {
			break
		}
		pageToken = result.NextPageToken
	}

	// Album not found
	return nil, fmt.Errorf("album %q not found", title)
}

// AddMediaItemToAlbum adds a media item to an album
func (c *Client) AddMediaItemToAlbum(ctx context.Context, mediaItem *MediaItem, album *Album) error {
	reqBody := map[string]interface{}{
		"mediaItemIds": []string{mediaItem.ID},
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		photosBaseURL+"/albums/"+album.ID+":batchAddMediaItems",
		bytes.NewReader(reqBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to add media item to album: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to add media item to album: %s", body)
	}

	return nil
}

// ValidateVideoFile checks if a file is valid for upload (Exported)
func (c *Client) ValidateVideoFile(filename string) error { // Renamed function
	// Check file exists and is readable
	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFile, err)
	}
	defer file.Close()

	// Get file info
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFile, err)
	}

	// Check file size
	if info.Size() < minVideoSize || info.Size() > maxVideoSize {
		return fmt.Errorf("%w: size %d is outside allowed range %d-%d",
			ErrInvalidFile, info.Size(), minVideoSize, maxVideoSize)
	}

	// Read first 512 bytes for MIME type detection
	header := make([]byte, 512)
	n, err := file.Read(header)
	if err != nil && err != io.EOF {
		return fmt.Errorf("%w: %v", ErrInvalidFile, err)
	}
	header = header[:n]

	// Check MIME type
	mimeType := http.DetectContentType(header)
	if !strings.HasPrefix(mimeType, "video/") {
		return fmt.Errorf("%w: invalid MIME type %s", ErrInvalidFile, mimeType)
	}

	return nil
}
