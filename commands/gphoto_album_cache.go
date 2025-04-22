package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const albumCacheFileName = "google_photos_album_cache.json"

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

	// TODO: is ids correct if there was a non-cached entry?
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
			// TODO: update cache for already cached names, in case the id changed.
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
