package commands

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	//"github.com/evanoberholster/imagemeta/xmp"
	"github.com/ccfrost/camflow/camflowconfig"
	"github.com/gphotosuploader/google-photos-api-client-go/v3/media_items"
	"github.com/schollz/progressbar/v3"
	"golang.org/x/time/rate"
)

type LocalConfig interface {
	GetExportQueueRoot() string
	GetExportedRoot() string
}

type GPConfig interface {
	GetDefaultAlbum() string
	GetLabelAlbums() []camflowconfig.KeyAlbum
	GetSubjectAlbums() []camflowconfig.KeyAlbum
}

// itemFileInfo stores path and size for progress tracking.
type itemFileInfo struct {
	path    string
	size    int64
	modTime time.Time
}

// scanExportQueue walks the export queue directory and returns the list of files to process,
// the total size of those files, and a slice of non-fatal warnings encountered during the walk.
func scanExportQueue(exportQueueDir string) ([]itemFileInfo, int64, error) {
	if _, err := os.Stat(exportQueueDir); os.IsNotExist(err) {
		return nil, 0, fmt.Errorf("export queue directory does not exist: %s", exportQueueDir)
	}

	var items []itemFileInfo
	var totalSize int64
	var numWalkErrors int
	err := filepath.WalkDir(exportQueueDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// If the error is about the root exportQueueDir itself not existing, propagate it.
			if path == exportQueueDir && os.IsNotExist(walkErr) {
				return fmt.Errorf("export queue directory '%s' disappeared or unreadable: %w", exportQueueDir, walkErr)
			}
			// For other errors (e.g. permission on a sub-file/dir), collect and continue.
			logger.Error("Error accessing path during walk, skipping",
				slog.String("path", path),
				slog.String("error", walkErr.Error()))
			numWalkErrors++
			return nil
		}

		if d.IsDir() || d.Name() == ".DS_Store" {
			return nil
		}

		info, statErr := d.Info()
		if statErr != nil {
			return fmt.Errorf("failed to get file info for %s: %w", path, statErr)
		}
		items = append(items, itemFileInfo{path: path, size: info.Size(), modTime: info.ModTime()})
		totalSize += info.Size()
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to walk export queue dir '%s': %w", exportQueueDir, err)
	}
	if numWalkErrors > 0 {
		logger.Warn("Encountered errors during directory walk, proceeding with successfully found files",
			slog.Int("error_count", numWalkErrors))
	}
	return items, totalSize, nil
}

// moveToExported moves a single media item from export queue to the exported directory.
// Returns the destination path.
func moveToExported(localConfig LocalConfig, fileInfo itemFileInfo) (string, error) {
	fileBasename := filepath.Base(fileInfo.path)

	year, month, day, err := parseDatePrefix(fileBasename)
	if err != nil {
		return "", fmt.Errorf("failed to parse date prefix from file name %s: %w", fileBasename, err)
	}
	relPath := filepath.Join(year, month, day, fileBasename)
	destPath := filepath.Join(localConfig.GetExportedRoot(), relPath)
	destDir := filepath.Dir(destPath)

	logger.Debug("Moving file",
		slog.String("from", fileInfo.path),
		slog.String("to", destPath))

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create destination directory %s for moving %s: %w", destDir, fileInfo.path, err)
	}

	// Destination collision handling
	if _, statErr := os.Stat(destPath); statErr == nil {
		return "", fmt.Errorf("failed to move %s: destination file %s already exists", fileInfo.path, destPath)
	} else if !os.IsNotExist(statErr) {
		return "", fmt.Errorf("failed to check destination %s: %w", destPath, statErr)
	}

	// Move the file
	sameFilesystem, err := isSameFilesystem(fileInfo.path, destDir)
	if err != nil {
		return "", fmt.Errorf("failed to check if source and destination are on the same filesystem: %w", err)
	}
	if sameFilesystem {
		if err := os.Rename(fileInfo.path, destPath); err != nil {
			return "", fmt.Errorf("failed to move %s from export queue to %s: %w", fileInfo.path, destPath, err)
		}
	} else {
		// Cross-filesystem move: copy then delete.
		// TOOD: clean up the possible .tmp file that could be left if this doesn't complete.
		if err := copyFile(fileInfo.path, destPath, fileInfo.size, fileInfo.modTime, nil /*bar*/); err != nil {
			return "", fmt.Errorf("failed to copy %s to %s: %w", fileInfo.path, destPath, err)
		}
		if err := os.Remove(fileInfo.path); err != nil {
			return "", fmt.Errorf("failed to remove original file %s after copying to %s: %w", fileInfo.path, destPath, err)
		}
	}
	logger.Debug("Successfully moved file",
		slog.String("from", fileInfo.path),
		slog.String("to", destPath))
	return destPath, nil
}

// uploadMediaItems uploads media items from the export queue dir to Google Photos.
// Media items are added to Google Photos album named DefaultAlbum.
// Uploaded media items are moved from export queue to exported dir; unless keepQueued is true, in which case they are copied (but not moved).
// The function is idempotent - if interrupted, it can be recalled to resume.
func uploadMediaItems(ctx context.Context, cacheDir string, keepQueued bool, localConfig LocalConfig, gpConfig GPConfig, itemTypePluralName string, gphotosClient GPhotosClient) error {
	exportQueueDir := localConfig.GetExportQueueRoot()
	if _, err := os.Stat(exportQueueDir); os.IsNotExist(err) {
		logger.Info("Export queue directory does not exist, nothing to upload",
			slog.String("export_queue_dir", exportQueueDir))
		return nil
	}

	// --- Initialize Rate Limiter ---
	// Limit to 5 operations per second, allowing bursts of up to 10.
	// TODO: check the actual rate limits for Google Photos API.
	limiter := rate.NewLimiter(rate.Every(time.Second/5), 10)

	itemsToUpload, totalSize, err := scanExportQueue(exportQueueDir)
	if err != nil {
		return err
	}

	if len(itemsToUpload) == 0 {
		logger.Info("No media items found in export queue directory",
			slog.String("export_queue_dir", exportQueueDir))
		return nil
	}
	logger.Info("Found files to upload",
		slog.Int("count", len(itemsToUpload)),
		slog.Float64("total_size_gb", math.Ceil(float64(totalSize)/1024/1024/1024)))

	if gpConfig.GetDefaultAlbum() == "" {
		logger.Warn("No default albums specified in config, files may only be uploaded to the library")
	}

	// Determine any additional albums to add each media item to based on the EXIF metadata.
	itemPaths := make([]string, len(itemsToUpload))
	for i, item := range itemsToUpload {
		itemPaths[i] = item.path
	}
	itemExifs, err := getExifMetadata(ctx, itemPaths)
	if err != nil {
		return err
	}
	additionalAlbumsPathToTitlesMap := make(map[string][]string)
	labelAlbums := gpConfig.GetLabelAlbums()
	subjectAlbums := gpConfig.GetSubjectAlbums()
	if len(labelAlbums) != 0 || len(subjectAlbums) != 0 {
		for _, exif := range itemExifs {
			if exif.Label != "" {
				if albumTitle, hasKey := albumForKey(labelAlbums, exif.Label); hasKey {
					additionalAlbumsPathToTitlesMap[exif.Path] = append(additionalAlbumsPathToTitlesMap[exif.Path], albumTitle)
				}
			}

			for _, subject := range exif.Subjects {
				if subject != "" {
					if albumTitle, hasKey := albumForKey(subjectAlbums, subject); hasKey {
						additionalAlbumsPathToTitlesMap[exif.Path] = append(additionalAlbumsPathToTitlesMap[exif.Path], albumTitle)
					}
				}
			}
		}
	}

	// Look up (and create any missing) album ids.

	albumCache, err := loadAlbumCache(getAlbumCachePath(cacheDir))
	if err != nil {
		return fmt.Errorf("failed to load album cache: %w", err)
	}

	albumTitlesMap := make(map[string]struct{})
	defaultAlbum := gpConfig.GetDefaultAlbum()
	if defaultAlbum != "" {
		albumTitlesMap[defaultAlbum] = struct{}{}
	}
	for _, albumTitles := range additionalAlbumsPathToTitlesMap {
		for _, albumTitle := range albumTitles {
			albumTitlesMap[albumTitle] = struct{}{}
		}
	}
	albumTitlesSlice := make([]string, 0, len(albumTitlesMap))
	for albumTitle := range albumTitlesMap {
		albumTitlesSlice = append(albumTitlesSlice, albumTitle)
	}

	var albumIDs []string
	albumTitleToIdMap := make(map[string]string)
	if len(albumTitlesSlice) > 0 {
		var err error
		albumIDs, err = albumCache.getOrFetchAndCreateAlbumIDs(ctx, gphotosClient.Albums(), albumTitlesSlice, limiter)
		if err != nil {
			return fmt.Errorf("failed to resolve or create album IDs for titles %v: %w", albumTitlesSlice, err)
		}
		logger.Debug("Target album IDs resolved/created",
			slog.Any("album_titles", albumTitlesSlice),
			slog.Any("album_ids", albumIDs))

		for i, albumID := range albumIDs {
			albumTitleToIdMap[albumTitlesSlice[i]] = albumID
		}
	}

	// Upload media items and add them to the target albums.

	bar := progressbar.DefaultBytes(
		totalSize,
		fmt.Sprint("Uploading ", itemTypePluralName),
	)

	// TODO: consider batching adding media items to albums. How to make it idempotent in face of failure part way through?
	for _, fileInfo := range itemsToUpload {
		additionalAlbumTitles := additionalAlbumsPathToTitlesMap[fileInfo.path]
		targetAlbumTitles := append(make([]string, 0, len(additionalAlbumTitles)+1), additionalAlbumTitles...)
		if defaultAlbum != "" {
			targetAlbumTitles = append(targetAlbumTitles, defaultAlbum)
		}
		if err := uploadMediaItem(ctx, keepQueued, localConfig, gphotosClient, fileInfo, targetAlbumTitles, albumTitleToIdMap, bar, limiter); err != nil {
			return fmt.Errorf("failed to upload media item %s: %w", fileInfo.path, err)
		}
	}

	_ = bar.Finish()

	logger.Debug(fmt.Sprint("Finished uploading ", itemTypePluralName))
	return nil
}

// uploadMediaItem uploads a single media item "filePath" of size "fileSize" to google photos.
// It updates "bar" with the bytes it has uploaded.
// It deletes the file after uploading if "keepQueued" is false.
// "targetAlbumIDs" are the ids for DefaultAlbums in the config.
// TODO: need albumNames and albumNameToIdMap.
func uploadMediaItem(ctx context.Context, keepQueued bool, localConfig LocalConfig, gphotosClient GPhotosClient, fileInfo itemFileInfo, targetAlbumTitles []string, albumTitleToIdMap map[string]string, bar *progressbar.ProgressBar, limiter *rate.Limiter) error {
	fileBasename := filepath.Base(fileInfo.path)
	bar.Describe(fmt.Sprintf("Uploading %s", fileBasename))

	// Defer the progress bar update to ensure it happens once per file attempt.
	defer bar.Add64(fileInfo.size)

	// Wait before uploading file
	if err := limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter error before uploading %s: %w", fileBasename, err)
	}

	// TODO: consider parallelizing uploads.
	// TODO: consider doing resumable uploads.
	// TODO: consider updating progress bar with actual upload progress. (gphotos UploadFile calls NewUploadFromFile, which returns a file, so it is close.)
	uploadToken, err := gphotosClient.Uploader().UploadFile(ctx, fileInfo.path)
	if err != nil {
		// TODO: only log error and skip? Want to make sure user notices.
		// fmt.Printf("\nError uploading file %s: %v. Skipping.\n", fileBasename, err)
		// return nil // Skip to the next item, progress bar will be updated by defer
		return fmt.Errorf("failed to upload file %s: %w", fileBasename, err)
	}

	if err := limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter error before creating media item for %s: %w", fileBasename, err)
	}
	simpleMediaItem := media_items.SimpleMediaItem{
		UploadToken: uploadToken,
		Filename:    fileBasename,
	}
	// TODO: consider batching media item creation.
	mediaItem, err := gphotosClient.MediaItems().Create(ctx, simpleMediaItem)
	if err != nil {
		logger.Error("Error creating media item, skipping",
			slog.String("file", fileBasename),
			slog.String("token", uploadToken),
			slog.String("error", err.Error()))
		return nil // Skip to the next item, progress bar will be updated by defer
	}
	logger.Debug("Successfully created media item",
		slog.String("file", fileBasename),
		slog.String("media_id", mediaItem.ID))

	// TODO: consider batch adding items to albums.
	for _, albumTitle := range targetAlbumTitles {
		albumID, ok := albumTitleToIdMap[albumTitle]
		if !ok {
			return fmt.Errorf("album '%s' not found in album ID map", albumTitle)
		}
		if err := limiter.Wait(ctx); err != nil {
			return fmt.Errorf("rate limiter error before adding %s to album %s: %w", fileBasename, albumTitle, err)
		}
		if err := gphotosClient.Albums().AddMediaItems(ctx, albumID, []string{mediaItem.ID}); err != nil {
			return fmt.Errorf("error adding media item to album %s: %w", albumTitle, err)
		}
		logger.Debug("Added media item to album",
			slog.String("media_id", mediaItem.ID),
			slog.String("album_title", albumTitle))

	}

	// Only move when keepQueued is false; uploading with keepQueued=true does not copy to exported.
	if !keepQueued {
		if _, err := moveToExported(localConfig, fileInfo); err != nil {
			return err
		}
	} else {
		logger.Debug("Keeping file in export queue directory as per keepQueued flag",
			slog.String("file", fileInfo.path))
	}

	return nil
}

// parseDatePrefix parses a basename "s" that is in the standard format of "YYYY-MM-DD-<rest-of-name>"
// and returns the year, month, and day parts.
func parseDatePrefix(s string) (year, month, day string, err error) {
	parts := strings.Split(s, "-")
	if len(parts) < 4 {
		return "", "", "", fmt.Errorf("invalid format: expected at least 4 parts separated by '-'")
	}
	if len(parts[0]) != 4 {
		return "", "", "", fmt.Errorf("invalid format: year '%s' must be 4 characters long", parts[0])
	}
	if len(parts[1]) != 2 {
		return "", "", "", fmt.Errorf("invalid format: month '%s' must be 2 characters long", parts[1])
	}
	if len(parts[2]) != 2 {
		return "", "", "", fmt.Errorf("invalid format: day '%s' must be 2 characters long", parts[2])
	}

	return parts[0], parts[1], parts[2], nil
}

// findExistingParent recursively walks up the directory tree to find a parent that exists.
func findExistingParent(rawPath string) (string, error) {
	absPath, err := filepath.Abs(rawPath)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path for %s: %w", rawPath, err)
	}

	current := absPath
	for {
		_, err := os.Stat(current)
		if err == nil {
			// Found a path that exists.
			return current, nil
		}
		if !os.IsNotExist(err) {
			// Some other error (eg, permission).
			return "", fmt.Errorf("error checking path %s: %w", current, err)
		}

		// Move to parent directory.
		parent := filepath.Dir(current)
		if parent == current {
			// Reached filesystem root - use it since any file created must be on this filesystem.
			return current, nil
		}
		current = parent
	}
}

// IsSameFileSystem_ForceFalse is a test-only variable that forces isSameFilesystem to return false.
// This allows testing cross-filesystem behavior even when source and destination are on the same filesystem.
var IsSameFileSystemForTests_ForceFalse bool

// isSameFilesystem checks if two paths are on the same filesystem.
// Handles cases where the paths don't exist yet by checking their existing parent directories.
func isSameFilesystem(path1, path2 string) (bool, error) {
	if IsSameFileSystemForTests_ForceFalse {
		return false, nil
	}

	existingPath1, err := findExistingParent(path1)
	if err != nil {
		return false, fmt.Errorf("failed to find existing parent for %s: %w", path1, err)
	}
	existingPath2, err := findExistingParent(path2)
	if err != nil {
		return false, fmt.Errorf("failed to find existing parent for %s: %w", path2, err)
	}

	// Note: if Stat fails because the path is a dangling symlink this returns an error.
	// That's okay because a later mkdir of that path would fail (I think), so there'd be
	// some work to do to support that case.
	stat1, err := os.Stat(existingPath1)
	if err != nil {
		return false, fmt.Errorf("failed to stat %s: %w", existingPath1, err)
	}
	stat2, err := os.Stat(existingPath2)
	if err != nil {
		return false, fmt.Errorf("failed to stat %s: %w", existingPath2, err)
	}

	stat1Sys, ok1 := stat1.Sys().(*syscall.Stat_t)
	stat2Sys, ok2 := stat2.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		return false, fmt.Errorf("unable to get filesystem device information for one/both of %s (%t) and %s (%t)", existingPath1, ok1, existingPath2, ok2)
	}
	return stat1Sys.Dev == stat2Sys.Dev, nil
}

// albumForKey returns the album name for the given key from the provided keyAlbums slice.
func albumForKey(keyAlbums []camflowconfig.KeyAlbum, key string) (string, bool) {
	for _, ka := range keyAlbums {
		if ka.Key == key {
			return ka.Album, true
		}
	}
	return "", false
}
