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
	"time"

	"github.com/ccfrost/camedia/camediaconfig"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	tokenFile       = "token.json"
	photosScope     = "https://www.googleapis.com/auth/photoslibrary"
	uploadScope     = "https://www.googleapis.com/auth/photoslibrary.appendonly"
	sharingScope    = "https://www.googleapis.com/auth/photoslibrary.sharing"
	photosBaseURL   = "https://photoslibrary.googleapis.com/v1"
	uploadChunkSize = 10 * 1024 * 1024        // 10MB chunks
	maxVideoSize    = 10 * 1024 * 1024 * 1024 // 10GB (Google Photos limit)
	minVideoSize    = 1                       // 1 byte minimum
	uploadTimeout   = 30 * time.Second        // Timeout for each chunk upload
)

// Error types for validation
var (
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
			photosScope,
			uploadScope,
			sharingScope,
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

	for info.BytesUploaded < info.TotalBytes {
		chunk := make([]byte, uploadChunkSize)
		n, err := f.Read(chunk)
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("failed to read chunk: %w", err)
		}
		if n == 0 {
			break
		}

		// Calculate content range
		rangeEnd := info.BytesUploaded + int64(n) - 1
		contentRange := fmt.Sprintf("bytes %d-%d/%d",
			info.BytesUploaded, rangeEnd, info.TotalBytes)

		// Create pipe to track chunk upload progress
		pr, pw := io.Pipe()
		chunkReader := &progressReader{
			reader: bytes.NewReader(chunk[:n]),
			size:   int64(n),
			progress: func(bytesRead int64) {
				if progressCb != nil {
					progressCb(UploadProgress{
						Filename:      info.Filename,
						BytesUploaded: info.BytesUploaded,
						TotalBytes:    info.TotalBytes,
						ChunkProgress: float64(bytesRead) / float64(n),
					})
				}
			},
		}
		go func() {
			_, err := io.Copy(pw, chunkReader)
			pw.CloseWithError(err)
		}()

		// Upload chunk
		req, err := http.NewRequestWithContext(ctx, "POST",
			info.UploadURL,
			pr)
		if err != nil {
			return "", err
		}

		req.Header.Set("X-Goog-Upload-Command", "upload")
		req.Header.Set("X-Goog-Upload-Offset", strconv.FormatInt(info.BytesUploaded, 10))
		req.Header.Set("Content-Range", contentRange)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("failed to upload chunk: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return "", fmt.Errorf("chunk upload failed with status %d: %s",
				resp.StatusCode, body)
		}

		info.BytesUploaded += int64(n)

		// Report full chunk completion
		if progressCb != nil {
			progressCb(UploadProgress{
				Filename:      info.Filename,
				BytesUploaded: info.BytesUploaded,
				TotalBytes:    info.TotalBytes,
				ChunkProgress: 1.0,
			})
		}

		// Save progress
		if err := c.saveUploadInfo(info); err != nil {
			fmt.Printf("warning: failed to save upload progress: %v\n", err)
		}

		// Check if this was the final chunk
		if resp.Header.Get("X-Goog-Upload-Status") == "final" {
			uploadToken, err := io.ReadAll(resp.Body)
			if err != nil {
				return "", fmt.Errorf("failed to read upload token: %w", err)
			}
			return string(uploadToken), nil
		}
	}

	return "", fmt.Errorf("upload completed without receiving upload token")
}

// progressReader wraps an io.Reader and reports read progress
type progressReader struct {
	reader   io.Reader
	size     int64
	read     int64
	progress func(int64)
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += int64(n)
	if r.progress != nil {
		r.progress(r.read)
	}
	return n, err
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

// GetOrCreateAlbum gets an album by title or creates it if it doesn't exist
func (c *Client) GetOrCreateAlbum(ctx context.Context, title string) (*Album, error) {
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

		var result struct {
			Albums        []Album `json:"albums"`
			NextPageToken string  `json:"nextPageToken"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
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

	// Album not found, create it
	reqBody := map[string]interface{}{
		"album": map[string]string{
			"title": title,
		},
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal album creation request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		photosBaseURL+"/albums",
		bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to create album: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to create album: %s", body)
	}

	var album Album
	if err := json.NewDecoder(resp.Body).Decode(&album); err != nil {
		return nil, fmt.Errorf("failed to decode created album: %w", err)
	}

	return &album, nil
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
