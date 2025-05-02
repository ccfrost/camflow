package commands

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/ccfrost/camedia/camediaconfig"
	"github.com/schollz/progressbar/v3"
)

type ImportDirEntry struct {
	RelativeDir string
	Count       int
}
type ImportResult struct {
	Photos []ImportDirEntry
	Videos []ImportDirEntry
}

// Import mvoes the DCIM/ files to the photo dir and the staging video dir.
// It returns the relative target directory for the photos and any error.
func Import(config camediaconfig.CamediaConfig, sdcardDir string, keepSrc bool, now time.Time) (ImportResult, error) {
	// Only look at files in $srcDir/DCIM/. Eg, ignore $srcDir/MISC/.
	srcDir := filepath.Join(sdcardDir, "DCIM")

	// TODO: Create todo “Process photos: <date> @ Photos” (which section?)

	files, totalSize, err := getFilesAndSize(srcDir)
	if err != nil {
		return ImportResult{}, fmt.Errorf("failed to list import files: %w", err)
	}

	// Check that there is sufficient space to move the files.
	targetAvailable, err := getAvailableSpace(config.MediaRoot)
	if err != nil {
		return ImportResult{}, fmt.Errorf("failed to get available space: %w", err)
	}
	const GiB = 1 << 30
	if uint64(totalSize) > targetAvailable {
		return ImportResult{}, fmt.Errorf(
			"not enough space in %s: need %d GiB more: %d GiB needed, %d GiB available",
			config.MediaRoot, totalSize/GiB, targetAvailable/GiB, (uint64(totalSize)-targetAvailable)/GiB)
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

	if !keepSrc {
		// Delete any leaf dirs that we moved files out of and are now empty, so that the
		// camera will restart the names of dirs that it writes files into.
		if err := deleteEmptyDirs(files); err != nil {
			return ImportResult{}, fmt.Errorf("failed to remove empty dirs: %w", err)
		}
	}

	// Eject the sdcard, because there is nothing else to do with it.
	cmd := exec.Command("diskutil", "eject", sdcardDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return ImportResult{}, fmt.Errorf("failed to eject disk at %s: %s, error: %w", sdcardDir, string(output), err)
	}
	fmt.Printf("Ejected sdcard\n")

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

// moveFiles moves files from srcDir into the staging photo/video dirs for the date of each file.
// It preserves the modification times.
func moveFiles(config camediaconfig.CamediaConfig, srcDir string, keepSrc bool, bar *progressbar.ProgressBar) (ImportResult, error) {
	photoDirs := make(map[string]int)
	videoDirs := make(map[string]int)

	photoStagingRoot := config.PhotoStagingDir()
	videoStagingRoot := config.VideoStagingDir()

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
		var stagingRoot string
		switch filepath.Ext(dirEnt.Name()) {
		case ".CR3", ".cr3", ".JPG", ".jpg":
			stagingRoot = photoStagingRoot
		case ".MP4", ".mp4":
			stagingRoot = videoStagingRoot
		default:
			// Skip unsupported file types.
			fmt.Printf("Skipping unsupported file: %s\n", path)
			return nil
		}

		// Compute target filename.
		info, err := dirEnt.Info()
		if err != nil {
			return fmt.Errorf("failed to Info() %s: %w", path, err)
		}
		relativeDir := info.ModTime().Format("2006/01/02")
		dirEntPrefix := info.ModTime().Format("2006-01-02-")
		targetPath := filepath.Join(stagingRoot, relativeDir, dirEntPrefix+dirEnt.Name())

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
	for dir, count := range photoDirs {
		result.Photos = append(result.Photos, ImportDirEntry{
			RelativeDir: dir,
			Count:       count,
		})
	}
	for dir, count := range videoDirs {
		result.Videos = append(result.Videos, ImportDirEntry{
			RelativeDir: dir,
			Count:       count,
		})
	}
	sort.Slice(result.Photos, func(i, j int) bool {
		return result.Photos[i].RelativeDir < result.Photos[j].RelativeDir
	})
	sort.Slice(result.Videos, func(i, j int) bool {
		return result.Videos[i].RelativeDir < result.Videos[j].RelativeDir
	})
	return result, nil
}

// copyFile creats a copy of src file at dstFinal.
// It creates the copy first a temporary file and then renames it to dstFinal.
// It shares its progress via bar.
func copyFile(src, dstFinal string, size int64, modTime time.Time, bar *progressbar.ProgressBar) error {
	dstTmp := dstFinal + ".tmp"

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	// Ensure target directories exists.
	baseName := filepath.Dir(dstFinal)
	if err := os.MkdirAll(baseName, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create dir %s: %w", baseName, err)
	}

	dstTmpFile, err := os.Create(dstTmp)
	if err != nil {
		return err
	}
	defer func() {
		if dstTmpFile != nil {
			dstTmpFile.Close()
		}
	}()

	buf := make([]byte, 1024*1024)

	for {
		n, err := srcFile.Read(buf)
		if err != nil && err != io.EOF {
			return fmt.Errorf("failed to read file %s: %w", src, err)
		}
		if n == 0 {
			break
		}

		if _, err := dstTmpFile.Write(buf[:n]); err != nil {
			return fmt.Errorf("failed to write file %s: %w", dstTmp, err)
		}

		bar.Add(n)
	}

	if err := dstTmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close dst tmp file %s: %w", dstTmp, err)
	}
	dstTmpFile = nil

	if err := os.Rename(dstTmp, dstFinal); err != nil {
		return fmt.Errorf("failed to rename %s: %w", dstTmp, err)
	}

	if err := os.Chtimes(dstFinal, modTime, modTime); err != nil {
		return err
	}

	return nil
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
