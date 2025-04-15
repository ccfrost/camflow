package commands

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/ccfrost/camedia/camediaconfig"
	"github.com/schollz/progressbar/v3"
)

// Import mvoes the DCIM/ files to the photo dir and the staging video dir.
// It returns the relative target directory for the photos and any error.
func Import(config camediaconfig.CamediaConfig, sdcardDir string, keepSrc bool, now time.Time) (string, error) {
	targetVidDir, err := videoStagingDir()
	if err != nil {
		return "", fmt.Errorf("failed to get video staging dir: %w", err)
	}
	// Create so that it exists for getAvailableSpace.
	if err := os.MkdirAll(targetVidDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create video staging dir: %w", err)
	}

	// Only look at files in $srcDir/DCIM/. Eg, ignore $srcDir/MISC/.
	srcDir := filepath.Join(sdcardDir, "DCIM")

	// TODO: Create todo “Process photos: <date> @ Photos” (which section?)

	// Pick directory name for the photos.
	// TODO: ?: change to date of most recent imported file, since the photos matter more than when they were imported.
	targetPhotoDirRelName := now.Format("2006/01/02")
	targetPhotoDir := filepath.Join(config.OrigPhotoRoot, targetPhotoDirRelName)

	files, totalSize, err := getFilesAndSize(srcDir)
	if err != nil {
		return "", fmt.Errorf("failed to list import files: %w", err)
	}

	// Check that there is sufficient space to move the files.
	// XXX: assumes target photo and video dirs are on the same filesystem.
	targetAvailable, err := getAvailableSpace(targetVidDir)
	if err != nil {
		return "", fmt.Errorf("failed to get available space: %w", err)
	}
	const GiB = 1 << 30
	if uint64(totalSize) > targetAvailable {
		return "", fmt.Errorf(
			"not enough space in %s: need %d GiB more: %d GiB needed, %d GiB available",
			targetVidDir, totalSize/GiB, targetAvailable/GiB, (uint64(totalSize)-targetAvailable)/GiB)
	}

	// Verify that there are no base filenames that appear multiple times.
	// This could happen if there are multiple directories under DCIM/ and
	// because this code flattens those directories.
	// Fixes seem like a lot of work (on this code and/or on the copied filenames),
	// so for now fail rather than silently drop such files or take on complications
	// to deal with them.
	if err := checkNoDupBasenames(files); err != nil {
		return "", err
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
	if err := moveFilesAndFlatten(srcDir, targetPhotoDir, targetVidDir, keepSrc, totalSize, bar); err != nil {
		return "", fmt.Errorf("failed to mvoe files: %w", err)
	}
	if err := bar.Close(); err != nil {
		fmt.Printf("warning: failed to close progress bar\n")
	}

	if !keepSrc {
		// Delete any leaf dirs that we moved files out of and are now empty, so that the
		// camera will restart the names of dirs that it writes files into.
		if err := deleteEmptyDirs(files); err != nil {
			return "", fmt.Errorf("failed to remove empty dirs: %w", err)
		}
	}

	// Eject the sdcard, because there is nothing else to do with it.
	cmd := exec.Command("diskutil", "eject", sdcardDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to eject disk at %s: %s, error: %w", sdcardDir, string(output), err)
	}
	fmt.Printf("Ejected sdcard\n")

	return targetPhotoDirRelName, nil
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

// checkNoDupBasenames checks that there are no duplicate basenames in the list of files.
func checkNoDupBasenames(files []string) error {
	basenames := make(map[string]struct{}, len(files))
	for _, f := range files {
		basename := filepath.Base(f)
		if _, exists := basenames[basename]; exists {
			return fmt.Errorf("at least two files have non-unique basenames; eg: %s", f)
		}
		basenames[basename] = struct{}{}
	}
	return nil
}

type moveProgress struct {
	startTime  time.Time
	movedBytes int64

	totalBytes int64

	bar *progressbar.ProgressBar
}

// moveFilesAndFlatten moves files from srcDir into the photo/video target dir.
// It preserves the modification times and flattens the directories.
func moveFilesAndFlatten(srcDir, targetPhotoDir, targetVidDir string, keepSrc bool, totalBytes int64, bar *progressbar.ProgressBar) error {
	// Ensure target directories exists.
	if err := os.MkdirAll(targetPhotoDir, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create photo dir %s: %w", targetPhotoDir, err)
	}
	if err := os.MkdirAll(targetVidDir, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create video staging dir %s: %w", targetVidDir, err)
	}

	return filepath.WalkDir(srcDir, func(path string, dirEnt fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if dirEnt.IsDir() {
			if filepath.Dir(path) == srcDir && !isDcimMediaDir(dirEnt.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		// Determine the target directory based on file extension.
		var targetDir string
		switch filepath.Ext(dirEnt.Name()) {
		case ".CR3", ".cr3", ".JPG", ".jpg":
			targetDir = targetPhotoDir
		case ".MP4", ".mp4":
			targetDir = targetVidDir
		default:
			// Skip unsupported file types.
			fmt.Printf("Skipping unsupported file: %s\n", path)
			return nil
		}

		targetPath := filepath.Join(targetDir, dirEnt.Name())
		info, err := dirEnt.Info()
		if err != nil {
			return fmt.Errorf("failed to Info() %s: %w", path, err)
		}
		if err := copyFile(path, targetPath, info.Size(), bar); err != nil {
			return err
		}
		if err := os.Chtimes(targetPath, info.ModTime(), info.ModTime()); err != nil {
			return err
		}

		if !keepSrc {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("failed to delete source file %s: %w", path, err)
			}
		}

		return nil
	})
}

// copyFile creats a copy of src file at dst.
// It shares its progress via bar.
func copyFile(src, dst string, size int64, bar *progressbar.ProgressBar) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	buf := make([]byte, 1024*1024)

	for {
		n, err := srcFile.Read(buf)
		if err != nil && err != io.EOF {
			return fmt.Errorf("failed to read file %s: %w", src, err)
		}
		if n == 0 {
			break
		}

		if _, err := dstFile.Write(buf[:n]); err != nil {
			return fmt.Errorf("failed to write file %s: %w", dst, err)
		}

		bar.Add(n)
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
