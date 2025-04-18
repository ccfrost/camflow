package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ccfrost/camedia/camediaconfig"
	gphotos "github.com/gphotosuploader/google-photos-api-client-go/v3"
	"github.com/gphotosuploader/google-photos-api-client-go/v3/media_items"
	"github.com/schollz/progressbar/v3"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	googlePhotosScope  = "https://www.googleapis.com/auth/photoslibrary.appendonly"
	tokenFileName      = "google_photos_token.json"
	albumCacheFileName = "google_photos_album_cache.json"
)

// --- OAuth2 & Client Setup ---

// getTokenFilePath constructs the path to the token file based on the config directory.
func getTokenFilePath(configDir string) (string, error) {
	if configDir == "." || configDir == "" {
		return "", fmt.Errorf("config directory path is empty or invalid")
	}
	return filepath.Join(configDir, tokenFileName), nil
}

// saveToken saves the OAuth2 token to the specified file path.
func saveToken(path string, token *oauth2.Token) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("unable to cache oauth token: %w", err)
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(token)
}

// getTokenFromWeb guides the user through the web-based OAuth2 flow.
func getTokenFromWeb(ctx context.Context, conf *oauth2.Config) (*oauth2.Token, error) {
	authURL := conf.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser then type the "+
		"authorization code: \n%v\n", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		return nil, fmt.Errorf("unable to read authorization code: %w", err)
	}

	tok, err := conf.Exchange(ctx, authCode)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve token from web: %w", err)
	}
	return tok, nil
}

// getAuthenticatedClient creates an authenticated HTTP client using OAuth2 credentials.
// It handles token loading, refreshing, and saving.
// Takes configDir to locate the token file.
func getAuthenticatedClient(ctx context.Context, config camediaconfig.CamediaConfig, configDir string) (*http.Client, error) {
	if config.GooglePhotos.ClientId == "" || config.GooglePhotos.ClientSecret == "" {
		return nil, fmt.Errorf("google Photos ClientId or ClientSecret not configured")
	}

	// Use http://localhost:0 for auto-selected port if RedirectURI is empty,
	// otherwise use the configured one.
	redirectURI := config.GooglePhotos.RedirectURI
	if redirectURI == "" {
		// Using a fixed common port for simplicity as dynamic port requires a listener.
		redirectURI = "http://localhost:8080"
		fmt.Printf("Warning: google_photos.redirect_uri not set in config, using default: %s\n", redirectURI)
	}

	conf := &oauth2.Config{
		ClientID:     config.GooglePhotos.ClientId,
		ClientSecret: config.GooglePhotos.ClientSecret,
		RedirectURL:  redirectURI,
		Scopes:       []string{googlePhotosScope},
		Endpoint:     google.Endpoint,
	}

	tokenFilePath, err := getTokenFilePath(configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get token file path: %w", err)
	}

	token := &oauth2.Token{}
	tokenFile, err := os.Open(tokenFilePath)
	if err == nil {
		err = json.NewDecoder(tokenFile).Decode(token)
		tokenFile.Close()
		if err != nil {
			fmt.Printf("Error reading token file (%s), requesting new token: %v\n", tokenFilePath, err)
			token = nil // Force getting a new token
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to open token file %s: %w", tokenFilePath, err)
	} else {
		// File does not exist, need to get token
		token = nil
	}

	if token == nil || !token.Valid() {
		fmt.Println("OAuth token is invalid or missing, starting auth flow...")
		newToken, err := getTokenFromWeb(ctx, conf)
		if err != nil {
			return nil, err
		}
		token = newToken
		if err := saveToken(tokenFilePath, token); err != nil {
			// Log error but continue, maybe token is still usable in memory
			fmt.Printf("Warning: Failed to save token to %s: %v\n", tokenFilePath, err)
		}
		fmt.Printf("Token obtained and saved successfully to %s\n", tokenFilePath)
	}

	// The gphotosuploader library expects an http.Client, which oauth2.Config provides.
	return conf.Client(ctx, token), nil
}

// --- Album Cache ---

// albumCache stores the mapping from album titles to album IDs.
type albumCache struct {
	Albums map[string]string `json:"albums"` // Title -> ID
	mu     sync.RWMutex
	path   string
}

// getAlbumCachePath constructs the path to the album cache file based on the config directory.
func getAlbumCachePath(configDir string) (string, error) {
	if configDir == "." || configDir == "" {
		return "", fmt.Errorf("config directory path is empty or invalid")
	}
	return filepath.Join(configDir, albumCacheFileName), nil
}

// loadAlbumCache loads the album cache from disk.
func loadAlbumCache(path string) (*albumCache, error) {
	cache := &albumCache{
		Albums: make(map[string]string),
		path:   path,
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cache, nil // Return empty cache if file doesn't exist
		}
		return nil, fmt.Errorf("failed to open album cache file %s: %w", path, err)
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(&cache); err != nil {
		// If decoding fails, log it and return an empty cache, forcing a refresh.
		fmt.Printf("Warning: Failed to decode album cache file %s, cache will be rebuilt: %v\n", path, err)
		cache.Albums = make(map[string]string) // Reset to empty map
	}
	return cache, nil
}

// save saves the album cache to disk.
func (c *albumCache) save() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	f, err := os.OpenFile(c.path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open album cache file %s for writing: %w", c.path, err)
	}
	defer f.Close()
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ") // Pretty print
	if err := encoder.Encode(c); err != nil {
		return fmt.Errorf("failed to encode album cache to %s: %w", c.path, err)
	}
	return nil
}

// getOrFetchAlbumIDs retrieves album IDs for the given titles, using the cache
// and fetching from the API if necessary. It returns an error if any album is not found.
func (c *albumCache) getOrFetchAlbumIDs(ctx context.Context, albumsService gphotos.AlbumsService, titles []string) ([]string, error) {
	c.mu.Lock() // Lock for potential modification
	defer c.mu.Unlock()

	ids := make([]string, 0, len(titles))
	missingTitles := make([]string, 0)
	titleSet := make(map[string]struct{}) // For quick lookup

	for _, title := range titles {
		if id, found := c.Albums[title]; found {
			ids = append(ids, id)
			titleSet[title] = struct{}{}
		} else {
			missingTitles = append(missingTitles, title)
			titleSet[title] = struct{}{}
		}
	}

	if len(missingTitles) == 0 {
		return ids, nil // All found in cache
	}

	fmt.Printf("Cache miss for albums: %v. Fetching from Google Photos...\n", missingTitles)

	// Fetch all albums from Google Photos API
	fetchedAlbums, err := albumsService.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list albums from Google Photos API: %w", err)
	}

	foundCount := 0
	needsSave := false
	for _, album := range fetchedAlbums {
		if _, needed := titleSet[album.Title]; needed {
			if _, alreadyCached := c.Albums[album.Title]; !alreadyCached {
				c.Albums[album.Title] = album.ID
				fmt.Printf("Found and cached album: '%s' (ID: %s)\n", album.Title, album.ID)
				needsSave = true
			}
			// Check if this fetched album was one we were missing
			for i, missing := range missingTitles {
				if album.Title == missing {
					ids = append(ids, album.ID)
					// Remove from missing list (swap with last element and shrink)
					missingTitles[i] = missingTitles[len(missingTitles)-1]
					missingTitles = missingTitles[:len(missingTitles)-1]
					foundCount++
					break
				}
			}
		}
	}

	// Save synchronously if changes were made
	if needsSave {
		fmt.Println("Saving updated album cache...")
		if err := c.save(); err != nil {
			// Return the save error, as it might indicate a persistent problem
			return nil, fmt.Errorf("error saving updated album cache: %w", err)
		}
	}

	// Check if any titles are still missing after fetching
	if len(missingTitles) > 0 {
		return nil, fmt.Errorf("the following albums were not found in Google Photos: %v", missingTitles)
	}

	return ids, nil
}

// --- Upload Logic ---

// videoFileInfo stores path and size for progress tracking.
type videoFileInfo struct {
	path string
	size int64
}

// UploadVideos uploads videos from the staging video dir to Google Photos.
// Videos are added to all albums in config.DefaultAlbums.
// Uploaded videos are deleted from staging unless keepStaging is true.
// The function is idempotent - if interrupted, it can be recalled to resume.
// Takes configDir to locate token and cache files.
func UploadVideos(ctx context.Context, config camediaconfig.CamediaConfig, configDir string, keepStaging bool) error {
	// Get staging directory
	stagingDir, err := videoStagingDir()
	if err != nil {
		return fmt.Errorf("failed to get video staging dir: %w", err)
	}

	// Check if staging dir exists
	if _, err := os.Stat(stagingDir); os.IsNotExist(err) {
		fmt.Println("Video staging directory does not exist, nothing to upload.")
		return nil
	}

	// List all video files in staging, store path and size, calculate total size
	var videosToUpload []videoFileInfo // Changed from []string
	var totalSize int64
	err = filepath.WalkDir(stagingDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err // Propagate errors during walk
		}
		if !d.IsDir() {
			// Basic check for common video extensions - adjusted to include MOV
			ext := filepath.Ext(path)
			// Convert to uppercase for case-insensitive comparison
			upperExt := strings.ToUpper(ext)
			if upperExt == ".MP4" || upperExt == ".MOV" {
				info, statErr := d.Info()
				if statErr != nil {
					fmt.Printf("Warning: Could not get file info for %s: %v\n", path, statErr)
					return nil // Continue walking, skip this file
				}
				// Store path and size
				videosToUpload = append(videosToUpload, videoFileInfo{path: path, size: info.Size()})
				totalSize += info.Size()
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to list staged videos: %w", err)
	}

	if len(videosToUpload) == 0 { // Check the new slice
		fmt.Println("No videos found in staging directory.")
		return nil
	}

	fmt.Printf("Found %d videos to upload (total size: %.2f MB).\n", len(videosToUpload), float64(totalSize)/1024/1024) // Use len of new slice

	// --- Initialize Google Photos Client ---
	httpClient, err := getAuthenticatedClient(ctx, config, configDir)
	if err != nil {
		return fmt.Errorf("failed to get authenticated Google Photos client: %w", err)
	}

	// Create Photos Library Client using the authenticated client
	gphotosClient, err := gphotos.NewClient(httpClient)
	if err != nil {
		return fmt.Errorf("failed to create Google Photos client: %w", err)
	}

	// --- Get Album IDs ---
	if len(config.DefaultAlbums) == 0 {
		fmt.Println("Warning: No default albums specified in config. Videos will only be uploaded to the library.")
	}

	albumCachePath, err := getAlbumCachePath(configDir)
	if err != nil {
		return fmt.Errorf("failed to get album cache path: %w", err)
	}
	cache, err := loadAlbumCache(albumCachePath)
	if err != nil {
		return fmt.Errorf("failed to load album cache: %w", err)
	}

	var targetAlbumIDs []string
	if len(config.DefaultAlbums) > 0 {
		targetAlbumIDs, err = cache.getOrFetchAlbumIDs(ctx, gphotosClient.Albums, config.DefaultAlbums)
		if err != nil {
			return fmt.Errorf("failed to resolve album IDs: %w", err) // Error includes unfound albums
		}
		fmt.Printf("Target album IDs resolved: %v\n", targetAlbumIDs)
	}

	// --- Upload Loop ---
	bar := progressbar.DefaultBytes(
		totalSize,
		"Uploading videos",
	)

	// Iterate over the struct slice
	for _, videoInfo := range videosToUpload {
		videoPath := videoInfo.path // Get path from struct
		fileSize := videoInfo.size  // Get size from struct
		filename := filepath.Base(videoPath)
		bar.Describe(fmt.Sprintf("Uploading %s", filename))

		// 1. Upload bytes using the client's uploader
		uploadToken, err := gphotosClient.Uploader.UploadFile(ctx, videoPath)
		if err != nil {
			// Don't stop the whole process, just log and continue
			fmt.Printf("\nError uploading file %s: %v. Skipping.\n", filename, err)
			// Use the stored size for progress update on failure
			bar.Add64(fileSize)
			continue // Skip to the next video
		}

		// 2. Create Media Item using the upload token
		// Construct the SimpleMediaItem required by the Create method
		simpleMediaItem := media_items.SimpleMediaItem{
			UploadToken: uploadToken,
			Filename:    filename,
		}
		// Pass the struct instead of individual arguments
		mediaItem, err := gphotosClient.MediaItems.Create(ctx, simpleMediaItem)
		if err != nil {
			fmt.Printf("\nError creating media item for %s (token: %s): %v. Skipping.\n", filename, uploadToken, err)
			// Use the stored size for progress update on failure
			bar.Add64(fileSize)
			continue
		}
		// Corrected: mediaItem.Id -> mediaItem.ID
		fmt.Printf("\nSuccessfully created media item for %s (ID: %s)\n", filename, mediaItem.ID)

		// 3. Add Media Item to Albums (if any specified)
		successfullyAddedToAll := true // Assume success unless an error occurs
		if len(targetAlbumIDs) > 0 {
			addedCount := 0
			failedAlbums := []string{}
			// Create a map for quick lookup of album titles by ID
			albumIDToTitle := make(map[string]string)
			for i, id := range targetAlbumIDs {
				if i < len(config.DefaultAlbums) { // Safety check
					albumIDToTitle[id] = config.DefaultAlbums[i]
				}
			}

			for _, albumID := range targetAlbumIDs {
				albumTitle := albumIDToTitle[albumID] // Get title for logging
				if albumTitle == "" {
					albumTitle = albumID
				} // Fallback to ID if title not found

				// Corrected: mediaItem.Id -> mediaItem.ID
				err = gphotosClient.Albums.AddMediaItems(ctx, albumID, []string{mediaItem.ID})
				if err != nil {
					// Corrected: mediaItem.Id -> mediaItem.ID
					fmt.Printf("Error adding media item %s to album '%s' (ID: %s): %v\n", mediaItem.ID, albumTitle, albumID, err)
					failedAlbums = append(failedAlbums, albumTitle)
					successfullyAddedToAll = false // Mark as failed
				} else {
					// Corrected: mediaItem.Id -> mediaItem.ID
					fmt.Printf("Added media item %s to album '%s'\n", mediaItem.ID, albumTitle)
					addedCount++
				}
			}
			if len(failedAlbums) > 0 {
				fmt.Printf("Warning: Failed to add %s to %d albums: %v\n", filename, len(failedAlbums), failedAlbums)
				// Decide if this is a critical failure. For now, we continue but don't delete.
			} else {
				fmt.Printf("Successfully added %s to all %d target albums.\n", filename, addedCount)
			}
		}

		// 4. Delete from staging if required and successful
		if successfullyAddedToAll && !keepStaging {
			fmt.Printf("Deleting %s from staging...\n", videoPath)
			if err := os.Remove(videoPath); err != nil {
				// Log error but don't fail the whole process
				fmt.Printf("Error deleting %s from staging: %v\n", videoPath, err)
			}
		} else if !successfullyAddedToAll {
			fmt.Printf("Skipping deletion of %s due to failure adding to some albums.\n", videoPath)
		} else if keepStaging {
			fmt.Printf("Keeping %s in staging directory.\n", videoPath)
		}

		// Update progress bar after processing the file (even if deletion failed)
		// Use the stored size directly
		bar.Add64(fileSize)

	} // End of video loop

	// Ensure progress bar finishes cleanly
	_ = bar.Finish() // Ignore error on finish

	fmt.Println("\nVideo upload process finished.")
	return nil
}

// videoStagingDir is defined in util.go
