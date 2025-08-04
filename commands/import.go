package commands

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/ccfrost/camflow/camflowconfig"
	"github.com/schollz/progressbar/v3"
)

type ImportDirEntry struct {
	RelativeDir string
	PhotoCount  int
	VideoCount  int
}
type ImportResult struct {
	SrcEntries []ImportDirEntry
	DstEntries []ImportDirEntry
}

// Import mvoes the DCIM/ files to the photo to process dir and the export queue video dir.
// It returns the relative target directory for the photos and any error.
func Import(config camflowconfig.CamflowConfig, sdcardDir string, keepSrc bool, now time.Time) (ImportResult, error) {
	if err := config.Validate(); err != nil {
		return ImportResult{}, fmt.Errorf("invalid config: %w", err)
	}

	// Only look at files in $srcDir/DCIM/. Eg, ignore $srcDir/MISC/.
	srcDir := filepath.Join(sdcardDir, "DCIM")

	// TODO: Create todo “Process photos: <date> @ Photos” (which section?)

	files, totalSize, err := getFilesAndSize(srcDir)
	if err != nil {
		return ImportResult{}, fmt.Errorf("failed to list import files: %w", err)
	}

	// Check that there is sufficient space to move the files.
	// TODO: check whether VideosExportQueueRoot is on the same filesystem as PhotosToProcessRoot
	// and check apppropriately.
	// TODO: when we move from export queue to exported, we should check that there is enough space?
	// TODO: or just remove this, and let the OS handle it?
	targetAvailable, err := getAvailableSpace(config.PhotosToProcessRoot)
	if err != nil {
		return ImportResult{}, fmt.Errorf("failed to get available space: %w", err)
	}

	if uint64(totalSize) > targetAvailable {
		const GiB = 1 << 30
		return ImportResult{}, fmt.Errorf(
			"not enough space in %s: need %d GiB more: %d GiB needed, %d GiB available",
			config.PhotosToProcessRoot, totalSize/GiB, targetAvailable/GiB, (uint64(totalSize)-targetAvailable)/GiB)
	}

	// Move the files into the target dirs.
	bar := progressbar.NewOptions64(totalSize,
		progressbar.OptionSetDescription("moving:"),
		progressbar.OptionSetWidth(20), // Fit in an 80-column terminal.
		progressbar.OptionShowBytes(true),
		progressbar.OptionUseIECUnits(true),
		progressbar.OptionShowCount(), // Show number of bytes moved.
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionShowTotalBytes(true),
		progressbar.OptionShowElapsedTimeOnFinish(),
	)
	importRes, err := moveFiles(config, srcDir, keepSrc, bar)
	if err != nil {
		return ImportResult{}, fmt.Errorf("failed to move files: %w", err)
	}
	if err := bar.Close(); err != nil {
		fmt.Printf("warning: failed to close progress bar\n")
	}
	fmt.Println() // End the progress bar line.

	if !keepSrc {
		// Delete any leaf dirs that we moved files out of and are now empty, so that the
		// camera will restart the names of dirs that it writes files into.
		if err := deleteEmptyDirs(files); err != nil {
			return ImportResult{}, fmt.Errorf("failed to remove empty dirs: %w", err)
		}
	}

	// Eject the sdcard, because there is nothing else to do with it.
	// Only attempt to eject if this appears to be a real mounted volume under /Volumes/
	if strings.HasPrefix(sdcardDir, "/Volumes/") {
		cmd := exec.Command("diskutil", "eject", sdcardDir)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return ImportResult{}, fmt.Errorf("failed to eject disk at %s: %s, error: %w", sdcardDir, string(output), err)
		}
		fmt.Printf("Ejected sdcard\n")
	} else {
		fmt.Printf("Skipping disk ejection for non-volume path: %s\n", sdcardDir)
	}

	return importRes, nil
}

// getFilesAndSize returns the list of all files in dir and sum of their sizes.
func getFilesAndSize(dir string) ([]string, int64, error) {
	var files []string
	var totalSize int64
	err := filepath.WalkDir(dir, func(path string, dirEnt fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if dirEnt.IsDir() {
			if filepath.Dir(path) == dir && !isDcimMediaDir(dirEnt.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(dirEnt.Name()) {
		case ".CR3", ".cr3", ".JPG", ".jpg", ".MP4", ".mp4":
			files = append(files, path)
			info, err := dirEnt.Info()
			if err != nil {
				return fmt.Errorf("failed to Info() %s: %w", path, err)
			}
			totalSize += info.Size()
		}
		return nil
	})

	return files, totalSize, err
}

// getAvailableSpace returns the available space in bytes on the filesystem
// containing the given directory path for the current user.
func getAvailableSpace(dir string) (uint64, error) {
	// Check if the directory exists first (Statfs might succeed on non-existent paths in some cases,
	// reporting stats for the parent, which might be confusing).
	// os.Stat also checks permissions implicitly.
	if _, err := os.Stat(dir); err != nil {
		return 0, fmt.Errorf("cannot stat directory %s: %w", dir, err)
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, fmt.Errorf("failed to get filesystem stats for %s: %w", dir, err)
	}

	// Calculate the available space = available blocks * block size.
	// stat.Bavail is the number of free blocks available to non-superusers.
	// stat.Bsize is the fundamental filesystem block size.
	// We cast Bsize to uint64 before multiplying to avoid potential overflow.
	availableBytes := stat.Bavail * uint64(stat.Bsize)

	return availableBytes, nil
}

// moveFiles moves files from srcDir into the photo/video dirs for the date of each file.
// It preserves the modification times.
func moveFiles(config camflowconfig.CamflowConfig, srcDir string, keepSrc bool, bar *progressbar.ProgressBar) (ImportResult, error) {
	// ItemType represents the type of media item being processed.
	type ItemType int
	const (
		ItemTypeUnknown ItemType = iota
		ItemTypePhoto
		ItemTypeVideo
	)
	// itemTypeString returns the string representation of ItemType for better debugging.
	itemTypeString := func(it ItemType) string {
		switch it {
		case ItemTypePhoto:
			return "photo"
		case ItemTypeVideo:
			return "video"
		default:
			return "unknown"
		}
	}

	type PhotoVideoCount struct {
		Photos int
		Videos int
	}
	srcDirCounts := make(map[string]PhotoVideoCount)
	dstDirCounts := make(map[string]PhotoVideoCount)

	err := filepath.WalkDir(srcDir, func(path string, dirEnt fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if dirEnt.IsDir() {
			if filepath.Dir(path) == srcDir && !isDcimMediaDir(dirEnt.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		// Determine photo vs video based on file extension.
		var targetRoot string
		var itemType ItemType
		switch filepath.Ext(dirEnt.Name()) {
		case ".CR3", ".cr3", ".JPG", ".jpg":
			targetRoot = config.PhotosToProcessRoot
			itemType = ItemTypePhoto
		case ".MP4", ".mp4":
			targetRoot = config.VideosExportQueueRoot
			itemType = ItemTypeVideo
		default:
			// Skip unsupported file types.
			fmt.Printf("Skipping unsupported file: %s\n", path)
			return nil
		}

		// Compute target filename and update counts.
		info, err := dirEnt.Info()
		if err != nil {
			return fmt.Errorf("failed to Info() %s: %w", path, err)
		}
		var targetPath string
		dirEntPrefix := info.ModTime().Format("2006-01-02-")
		srcEntry := srcDirCounts[filepath.Dir(path)]
		switch itemType {
		case ItemTypePhoto:
			relativeDir := info.ModTime().Format("2006/01/02")
			targetPath = filepath.Join(targetRoot, relativeDir, dirEntPrefix+dirEnt.Name())

			dstEntry := dstDirCounts[relativeDir]
			srcEntry.Photos++
			dstEntry.Photos++

			dstDirCounts[relativeDir] = dstEntry
		case ItemTypeVideo:
			targetPath = filepath.Join(targetRoot, dirEntPrefix+dirEnt.Name())

			srcEntry.Videos++
		default:
			return fmt.Errorf("unexpected item type %s for file %s", itemTypeString(itemType), path)
		}
		srcDirCounts[filepath.Dir(path)] = srcEntry

		// Note: this assumes that there are no duplicate camera file names created on the same day.
		// That could happen, eg if the camera's counter is reset or if enough photos are taken in that day,
		// but it is unlikely enough that we ignore it for now.
		if err := copyFile(path, targetPath, info.Size(), info.ModTime(), bar); err != nil {
			return err
		}

		if !keepSrc {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("failed to delete source file %s: %w", path, err)
			}
		}

		return nil
	})
	if err != nil {
		return ImportResult{}, err
	}

	var result ImportResult

	for dir, entry := range srcDirCounts {
		result.SrcEntries = append(result.SrcEntries, ImportDirEntry{
			RelativeDir: dir,
			PhotoCount:  entry.Photos,
			VideoCount:  entry.Videos,
		})
	}
	sort.Slice(result.SrcEntries, func(i, j int) bool {
		return result.SrcEntries[i].RelativeDir < result.SrcEntries[j].RelativeDir
	})

	for dir, entry := range dstDirCounts {
		result.DstEntries = append(result.DstEntries, ImportDirEntry{
			RelativeDir: dir,
			PhotoCount:  entry.Photos,
			VideoCount:  entry.Videos,
		})
	}
	sort.Slice(result.DstEntries, func(i, j int) bool {
		return result.DstEntries[i].RelativeDir < result.DstEntries[j].RelativeDir
	})

	return result, nil
}

// isDcimMediaDir returns whether the DCIM standard says that name
// can contain camera media files. This function expects that name
// is the name of a directory in DCIM/.
func isDcimMediaDir(name string) bool {
	if len(name) < 4 {
		return false
	}
	return isAllDigits(name[:3])
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// deleteEmptyDirs removes empty directories in the list of files.
func deleteEmptyDirs(files []string) error {
	dirs := make(map[string]struct{})
	for _, f := range files {
		dir := filepath.Dir(f)
		dirs[dir] = struct{}{}
	}

	for dir := range dirs {
		if err := os.Remove(dir); err != nil {
			// Ignore "directory not empty" errors.
			if !os.IsNotExist(err) && !strings.Contains(err.Error(), "directory not empty") {
				return fmt.Errorf("failed to remove directory %s: %w", dir, err)
			}
		}
	}
	return nil
}
