package googlephotos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/ccfrost/camedia/camediaconfig"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	tokenFile     = "token.json"
	photosScope   = "https://www.googleapis.com/auth/photoslibrary"
	uploadScope   = "https://www.googleapis.com/auth/photoslibrary.appendonly"
	sharingScope  = "https://www.googleapis.com/auth/photoslibrary.sharing"
	photosBaseURL = "https://photoslibrary.googleapis.com/v1"
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

// UploadVideo uploads a video file to Google Photos
func (c *Client) UploadVideo(ctx context.Context, filename string) (*MediaItem, error) {
	// First upload the bytes
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open video file: %w", err)
	}
	defer f.Close()

	uploadToken, err := c.uploadBytes(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("failed to upload bytes: %w", err)
	}

	// Create the media item using the upload token
	reqBody := map[string]interface{}{
		"newMediaItems": []map[string]interface{}{
			{
				"description": filepath.Base(filename),
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

// uploadBytes uploads raw bytes to Google Photos and returns an upload token
func (c *Client) uploadBytes(ctx context.Context, r io.Reader) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "POST",
		photosBaseURL+"/uploads",
		r)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Goog-Upload-Protocol", "raw")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("upload failed with status %d: %s", resp.StatusCode, body)
	}

	uploadToken, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(uploadToken), nil
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
