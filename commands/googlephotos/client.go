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
	if err := c.validateVideoFile(filename); err != nil {
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

	// Seek to the last uploaded position
	if _, err := f.Seek(info.BytesUploaded, 0); err != nil {
		return "", fmt.Errorf("failed to seek in file: %w", err)
	}

	chunk := make([]byte, uploadChunkSize) // Reusable buffer

	for info.BytesUploaded < info.TotalBytes {
		// Determine chunk size for this iteration
		bytesToRead := uploadChunkSize
		remainingBytes := info.TotalBytes - info.BytesUploaded
		if int64(bytesToRead) > remainingBytes {
			bytesToRead = int(remainingBytes)
		}

		// Read the exact chunk size needed
		n, err := io.ReadFull(f, chunk[:bytesToRead])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF { // Allow EOF/UnexpectedEOF if it matches bytes read
			return "", fmt.Errorf("failed to read chunk: %w", err)
		}
		if n == 0 { // Should not happen if BytesUploaded < TotalBytes, but check anyway
			break
		}

		// Calculate content range
		rangeEnd := info.BytesUploaded + int64(n) - 1
		contentRange := fmt.Sprintf("bytes %d-%d/%d",
			info.BytesUploaded, rangeEnd, info.TotalBytes)

		// Create request with the chunk data directly
		req, err := http.NewRequestWithContext(ctx, "POST",
			info.UploadURL,
			bytes.NewReader(chunk[:n])) // Use bytes.NewReader for the chunk data
		if err != nil {
			// If context is cancelled, return the context error directly
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return "", err
			}
			return "", fmt.Errorf("failed to create chunk request: %w", err)
		}

		req.Header.Set("X-Goog-Upload-Command", "upload")
		req.Header.Set("X-Goog-Upload-Offset", strconv.FormatInt(info.BytesUploaded, 10))
		req.Header.Set("Content-Range", contentRange)
		// Content-Length is set automatically by the http client for bytes.Reader

		resp, err := c.httpClient.Do(req)
		if err != nil {
			// If context is cancelled, return the context error directly
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return "", err
			}
			// Consider retrying certain errors here? For now, fail.
			return "", fmt.Errorf("failed to upload chunk: %w", err)
		}
		defer resp.Body.Close() // Close body for each chunk response

		// Google Resumable Upload uses 308 for intermediate, 200/201 for final.
		// Let's accept both 200 OK and 308 Resume Incomplete as success for now.
		// The mock server currently sends 200 OK for both.
		if resp.StatusCode != http.StatusOK && resp.StatusCode != 308 {
			body, _ := io.ReadAll(resp.Body)
			// Check if the server indicates a range mismatch (e.g., status 400 or other)
			// Potentially query the server for expected offset if status indicates error?
			return "", fmt.Errorf("chunk upload failed with status %d: %s",
				resp.StatusCode, body)
		}

		// Update bytes uploaded *after* successful transmission
		info.BytesUploaded += int64(n)

		// Report progress *after* successful chunk upload
		if progressCb != nil {
			progressCb(UploadProgress{
				Filename:      info.Filename,
				BytesUploaded: info.BytesUploaded,
				TotalBytes:    info.TotalBytes,
				ChunkProgress: 1.0, // Indicate chunk is fully processed by client
			})
		}

		// Save progress
		if err := c.saveUploadInfo(info); err != nil {
			// Log warning but continue? Or fail hard? For now, log and continue.
			fmt.Printf("warning: failed to save upload progress: %v\n", err)
		}

		// Check if this was the final chunk based on status code or total bytes
		// Google uses 200 OK or 201 Created for the final response.
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated || info.BytesUploaded >= info.TotalBytes {
			// Read the response body which should contain the upload token for 200/201
			// If it was 308 but we reached TotalBytes, we still need to finalize/query?
			// The Google API doc implies the final chunk response (200/201) contains the item details or token.
			// Let's assume 200/201 means final and contains the token.
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
				uploadTokenBytes, err := io.ReadAll(resp.Body)
				if err != nil {
					return "", fmt.Errorf("failed to read upload token from final response: %w", err)
				}
				if len(uploadTokenBytes) == 0 {
					// This might happen if the server sent 200 OK but no body,
					// which would be unexpected for the final Google Photos response.
					// Maybe query the upload status first?
					return "", fmt.Errorf("received empty upload token in final response")
				}
				return string(uploadTokenBytes), nil
			} else if info.BytesUploaded >= info.TotalBytes {
				// We've sent all bytes, but the last response was 308.
				// This might indicate the server hasn't processed the final chunk yet.
				// We might need a final "finalize" request or query the status.
				// For simplicity now, let's assume if we sent all bytes, it should have finished.
				// This path indicates a potential mismatch with the real API or the mock.
				// Let's try querying the upload status.
				finalStatus, finalToken, queryErr := c.queryUploadStatus(ctx, info)
				if queryErr != nil {
					return "", fmt.Errorf("upload seemingly complete but failed to query final status: %w", queryErr)
				}
				if finalStatus == "final" && finalToken != "" {
					return finalToken, nil
				}
				return "", fmt.Errorf("upload complete but final status query returned status '%s' or empty token", finalStatus)
			}
		}
		// If it was 308 and not the last chunk, continue the loop.
	}

	// If the loop finishes without returning a token (e.g., TotalBytes is 0, or some edge case)
	if info.TotalBytes == 0 {
		return "", fmt.Errorf("cannot upload zero-byte file")
	}
	return "", fmt.Errorf("upload loop finished unexpectedly without receiving upload token")
}

// queryUploadStatus checks the status of a resumable upload.
// Returns status ("active", "final", etc.), upload token (if final), and error.
func (c *Client) queryUploadStatus(ctx context.Context, info *UploadInfo) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", info.UploadURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to create query request: %w", err)
	}
	req.Header.Set("X-Goog-Upload-Command", "query")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("failed to query upload status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != 308 { // Expect 200 or 308 for query
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("query upload status failed with status %d: %s", resp.StatusCode, body)
	}

	status := resp.Header.Get("X-Goog-Upload-Status")
	token := ""
	if status == "final" {
		// If final, the body might contain the token (though docs are unclear if query returns it)
		// Let's assume the token is only in the final *upload* response body.
		// If the status is final, the caller should use the token received previously.
		// However, our mock *does* put the token in the body on final upload.
		// Let's read it just in case, aligning with the mock for now.
		tokenBytes, _ := io.ReadAll(resp.Body)
		token = string(tokenBytes)
	}

	// We might also want to check X-Goog-Upload-Size-Received header here to verify server state.

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
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var info UploadInfo
	if err := json.NewDecoder(f).Decode(&info); err != nil {
		return nil, err
	}

	// Verify the file still exists and has the same size
	fileInfo, err := os.Stat(filename)
	if err != nil {
		return nil, err
	}
	if fileInfo.Size() != info.TotalBytes {
		return nil, fmt.Errorf("file size changed")
	}

	return &info, nil
}

func (c *Client) saveUploadInfo(info *UploadInfo) error {
	path, err := getUploadInfoPath(info.Filename)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create upload info file: %w", err)
	}
	defer f.Close()

	return json.NewEncoder(f).Encode(info)
}

func (c *Client) deleteUploadInfo(filename string) error {
	path, err := getUploadInfoPath(filename)
	if err != nil {
		return err
	}
	return os.Remove(path)
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

// validateVideoFile checks if a file is valid for upload
func (c *Client) validateVideoFile(filename string) error {
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
