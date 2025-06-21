package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/time/rate"
)

// albumCache stores the mapping from album titles to album IDs.
type albumCache struct {
	Albums map[string]string `json:"albums"` // Title -> ID
	mu     sync.RWMutex
	path   string
}

// getAlbumCachePath constructs the path to the album cache file.
func getAlbumCachePath(cacheDir string) string {
	return filepath.Join(cacheDir, "google_photos_album_cache.json")
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
		return nil, fmt.Errorf("failed to decode album cache file %s: %w", path, err)
	}
	// Successfully decoded. Check if cache.Albums is nil (e.g. due to "albums": null in JSON)
	// This can happen if the JSON file explicitly sets the 'albums' key to null.
	if cache.Albums == nil {
		fmt.Printf("Warning: Album cache file %s decoded successfully, but 'albums' field was null. Initializing as empty map.\n", path)
		cache.Albums = make(map[string]string)
	}
	return cache, nil
}

// save saves the album cache to disk.
// The caller (getOrFetchAndCreateAlbumIDs) is expected to hold c.mu.Lock().
func (c *albumCache) save() error {
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

// getOrFetchAndCreateAlbumIDs retrieves album IDs for the given titles,
// using the cache, fetching from the API, or creating them if necessary.
// It uses a rate limiter for API calls and preserves the order of IDs.
func (c *albumCache) getOrFetchAndCreateAlbumIDs(
	ctx context.Context,
	albumsService AppAlbumsService, // Changed to AppAlbumsService
	titles []string,
	limiter *rate.Limiter,
) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	finalIDs := make([]string, len(titles))
	titlesToProcessMap := make(map[string]int) // title -> original index
	processedCount := 0

	// 1. Check cache first and prepare for processing
	for i, title := range titles {
		if id, found := c.Albums[title]; found {
			finalIDs[i] = id
			processedCount++
		} else {
			titlesToProcessMap[title] = i // Store original index for later placement
		}
	}

	if processedCount == len(titles) {
		return finalIDs, nil // All found in cache and correctly ordered
	}

	fmt.Printf("Cache miss for some albums. Titles needing processing: %v. Fetching from Google Photos...\n", getKeys(titlesToProcessMap))
	needsSave := false

	// 2. Fetch all albums from Google Photos API to find existing ones among titlesToProcessMap
	if err := limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limiter error before listing albums: %w", err)
	}
	fetchedAlbums, err := albumsService.List(ctx) // Removed opts ...albums.ListOption
	if err != nil {
		return nil, fmt.Errorf("failed to list albums from Google Photos API: %w", err)
	}

	for _, album := range fetchedAlbums { // Iterate directly over the slice
		if originalIndex, needed := titlesToProcessMap[album.Title]; needed {
			fmt.Printf("Found album online: '%s' (ID: %s)\n", album.Title, album.ID)
			c.Albums[album.Title] = album.ID // Update cache
			finalIDs[originalIndex] = album.ID
			delete(titlesToProcessMap, album.Title) // Mark as processed
			needsSave = true
			processedCount++
		}
	}

	// 3. Create albums that are still in titlesToProcessMap (i.e., not cached, not found online)
	for titleToCreate, originalIndex := range titlesToProcessMap {
		fmt.Printf("Album '%s' not found in cache or online. Creating...\n", titleToCreate)
		if err := limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limiter error before creating album '%s': %w", titleToCreate, err)
		}
		newAlbum, err := albumsService.Create(ctx, titleToCreate) // Removed options ...albums.CreateOption
		if err != nil {
			// If creation fails, this is a significant issue for the intended operation.
			return nil, fmt.Errorf("failed to create album '%s': %w", titleToCreate, err)
		}
		fmt.Printf("Successfully created and cached album: '%s' (ID: %s)\n", newAlbum.Title, newAlbum.ID)
		c.Albums[newAlbum.Title] = newAlbum.ID
		finalIDs[originalIndex] = newAlbum.ID
		// No need to delete from titlesToProcessMap here as we are iterating over it
		needsSave = true
		processedCount++
	}

	// 4. Save cache if any changes were made
	if needsSave {
		fmt.Println("Saving updated album cache...")
		if err := c.save(); err != nil {
			return nil, fmt.Errorf("error saving updated album cache: %w", err)
		}
	}

	// Final check: ensure all titles were processed and have an ID.
	if processedCount != len(titles) {
		// This indicates a logic error or an unhandled case.
		// Collect missing titles for a more informative error.
		missingDebug := []string{}
		for i, id := range finalIDs {
			if id == "" {
				missingDebug = append(missingDebug, titles[i])
			}
		}
		return finalIDs, fmt.Errorf("could not resolve all album titles; expected %d IDs, processed %d. Missing for: %v", len(titles), processedCount, missingDebug)
	}

	return finalIDs, nil
}

// Helper function to get keys from a map for printing (order not guaranteed)
func getKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
