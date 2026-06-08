package lib

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ExifData holds the extracted metadata for a single file.
type ExifData struct {
	Path     string
	Label    string
	Subjects []string
}

// getExifMetadata extracts Label and Subject metadata from a list of files using exiftool.
// TODO: write a test for this.
func getExifMetadata(ctx context.Context, paths []string) ([]ExifData, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	exiftoolPath, err := exec.LookPath("exiftool")
	if err != nil {
		return nil, fmt.Errorf("exiftool not found in PATH: %w", err)
	}

	args := []string{"-j", "-Label", "-Subject"}
	args = append(args, paths...)

	cmd := exec.CommandContext(ctx, exiftoolPath, args...)
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("failed to run exiftool: %w", err)
	}

	var results []struct {
		SourceFile string `json:"SourceFile"`
		Label      string `json:"Label,omitempty"`
		Subject    any    `json:"Subject,omitempty"` // Subject can be a string or []any.
	}
	if err := json.Unmarshal(output, &results); err != nil {
		return nil, fmt.Errorf("failed to unmarshal exiftool output: %w", err)
	}

	var exifData []ExifData
	for _, r := range results {
		data := ExifData{
			Path:  r.SourceFile,
			Label: r.Label,
		}
		switch s := r.Subject.(type) {
		case string:
			data.Subjects = []string{s}
		case []any:
			for _, item := range s {
				if strItem, ok := item.(string); ok {
					data.Subjects = append(data.Subjects, strItem)
				}
			}
		}
		exifData = append(exifData, data)
	}

	return exifData, nil
}

func printNameIfMatch(ctx context.Context, path, label, subject string) error {
	if label == "" && subject == "" {
		return nil
	}

	results, err := getExifMetadata(ctx, []string{path})
	if err != nil {
		return fmt.Errorf("failed to get exif metadata for %s: %w", path, err)
	}

	for _, data := range results {
		if label != "" && data.Label == label {
			fmt.Println("label:", data.Path)
		}

		if subject != "" {
			for _, s := range data.Subjects {
				if s == subject {
					fmt.Println("subject:", data.Path)
					break // Print only once per file for subject match
				}
			}
		}
	}

	return nil
}

func PrintNameIfMatch(ctx context.Context, path, label, subject string) error {
	// This is a public function to allow testing.
	return printNameIfMatch(ctx, path, label, subject)
}

// ImageStabilizationResult holds the IS check result for one file
type ImageStabilizationResult struct {
	FilePath string
	HasIS    bool
	Error    error
}

// checkImageStabilizationBatch checks that Image Stabilization was used in a batch of CR3 files.
func checkImageStabilizationBatch(ctx context.Context, paths []string) ([]ImageStabilizationResult, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	exiftoolPath, err := exec.LookPath("exiftool")
	if err != nil {
		return nil, fmt.Errorf("exiftool not found in PATH: %w", err)
	}

	args := []string{"-j", "-ImageStabilization"}
	args = append(args, paths...)

	cmd := exec.CommandContext(ctx, exiftoolPath, args...)
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("failed to run exiftool: %w", err)
	}

	var exifResults []struct {
		SourceFile         string `json:"SourceFile"`
		ImageStabilization string `json:"ImageStabilization,omitempty"`
	}
	if err := json.Unmarshal(output, &exifResults); err != nil {
		return nil, fmt.Errorf("failed to unmarshal exiftool output: %w", err)
	}

	var results []ImageStabilizationResult
	for _, r := range exifResults {
		result := ImageStabilizationResult{FilePath: r.SourceFile}
		if r.ImageStabilization == "" {
			result.Error = fmt.Errorf("ImageStabilization field not found")
		} else {
			hasIS, parseErr := parseImageStabilizationValue(r.ImageStabilization)
			if parseErr != nil {
				result.Error = parseErr
			} else {
				result.HasIS = hasIS
			}
		}
		results = append(results, result)
	}

	return results, nil
}

// parseImageStabilizationValue parses the ImageStabilization field value
// Example values: "On (2)", "Off", "On"
func parseImageStabilizationValue(value string) (bool, error) {
	// Look for "On" or "Off" at the beginning of the value
	re := regexp.MustCompile(`(?i)^(on|off)`)
	matches := re.FindStringSubmatch(strings.TrimSpace(value))
	if len(matches) < 2 {
		return false, fmt.Errorf("could not parse Image Stabilization value: %q", value)
	}

	status := strings.ToLower(matches[1])
	return status == "on", nil
}

// videoTimezoneExif holds the timezone-relevant metadata for a single video file.
type videoTimezoneExif struct {
	Path               string // exiftool SourceFile, mirrors ExifData.Path
	DateTimeOriginal   string // Canon DateTimeOriginal, e.g. "2026:04:03 16:37:51"
	OffsetTimeOriginal string // Canon OffsetTimeOriginal, e.g. "-08:00"
	CreationDate       string // com.apple.quicktime.creationdate (Keys:CreationDate)
}

// getVideoTimezoneExifFn and prepareVideoForUploadFn are package-level indirections so
// tests can stub the exiftool-backed behavior.
var getVideoTimezoneExifFn = getVideoTimezoneExif
var prepareVideoForUploadFn = prepareVideoForUpload

// getVideoTimezoneExif reads the timezone-relevant metadata for a batch of video files
// using a single exiftool invocation. It mirrors getExifMetadata: the exiftool JSON
// SourceFile is unmarshaled into the struct's Path field, so all downstream matching is
// against Path.
func getVideoTimezoneExif(ctx context.Context, paths []string) ([]videoTimezoneExif, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	exiftoolPath, err := exec.LookPath("exiftool")
	if err != nil {
		return nil, fmt.Errorf("exiftool not found in PATH: %w", err)
	}

	args := []string{"-j", "-DateTimeOriginal", "-OffsetTimeOriginal", "-Keys:CreationDate"}
	args = append(args, paths...)

	cmd := exec.CommandContext(ctx, exiftoolPath, args...)
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("failed to run exiftool: %w", err)
	}

	var results []struct {
		SourceFile         string `json:"SourceFile"`
		DateTimeOriginal   string `json:"DateTimeOriginal,omitempty"`
		OffsetTimeOriginal string `json:"OffsetTimeOriginal,omitempty"`
		CreationDate       string `json:"CreationDate,omitempty"`
	}
	if err := json.Unmarshal(output, &results); err != nil {
		return nil, fmt.Errorf("failed to unmarshal exiftool output: %w", err)
	}

	exifData := make([]videoTimezoneExif, 0, len(results))
	for _, r := range results {
		exifData = append(exifData, videoTimezoneExif{
			Path:               r.SourceFile,
			DateTimeOriginal:   r.DateTimeOriginal,
			OffsetTimeOriginal: r.OffsetTimeOriginal,
			CreationDate:       r.CreationDate,
		})
	}

	return exifData, nil
}

// explicitTimezoneRe matches a trailing explicit timezone on an exiftool timestamp:
// either "Z" or a numeric offset like "-08:00", "+02:00", or the colon-less "-0800".
var explicitTimezoneRe = regexp.MustCompile(`(Z|[+-]\d{2}:?\d{2})$`)

// hasExplicitTimezone reports whether value ends in an explicit timezone (Z or a numeric
// offset, with or without a colon). Empty or offset-less timestamps return false.
func hasExplicitTimezone(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	return explicitTimezoneRe.MatchString(value)
}

// normalizeTimestampWithOffset parses an exiftool timestamp carrying an explicit offset
// (e.g. "2026:04:03 16:37:51-08:00") into a canonical "YYYY:MM:DD HH:MM:SS±HH:MM" string
// preserving both the local wall-clock time and the offset. A "Z" offset is normalized to
// "+00:00". Sub-seconds are accepted but dropped, so matching is at second precision — a
// sub-second-only difference does not count as a mismatch.
func normalizeTimestampWithOffset(value string) (string, bool) {
	value = strings.TrimSpace(value)
	loc := explicitTimezoneRe.FindStringIndex(value)
	if loc == nil {
		return "", false
	}
	base := strings.TrimSpace(value[:loc[0]])
	offset := value[loc[0]:]
	switch {
	case offset == "Z":
		offset = "+00:00"
	case !strings.Contains(offset, ":"):
		// Canonicalize a colon-less offset (e.g. "-0800") to "-08:00" so equality holds
		// against colon-bearing offsets.
		offset = offset[:3] + ":" + offset[3:]
	}
	t, err := time.Parse("2006:01:02 15:04:05.999999999", base)
	if err != nil {
		return "", false
	}
	return t.Format("2006:01:02 15:04:05") + offset, true
}

// creationDateMatchesCanon reports whether an existing CreationDate atom encodes the same
// local wall-clock time AND offset as Canon's DateTimeOriginal+OffsetTimeOriginal. It is
// intentionally stricter than an instant comparison: an atom sharing Canon's UTC instant
// but carrying a different offset (e.g. "...00:37:00Z" vs Canon "...16:37:00-08:00") does
// not match, because Google would still display it at the wrong local time.
func creationDateMatchesCanon(creationDate, dto, oto string) bool {
	cd, ok := normalizeTimestampWithOffset(creationDate)
	if !ok {
		return false
	}
	canon, ok := normalizeTimestampWithOffset(dto + oto)
	if !ok {
		return false
	}
	return cd == canon
}

// prepareVideoForUpload returns the path to upload for a single video, plus a cleanup
// function the caller must defer. When the file already carries a correct Apple
// creationdate atom it returns the original path with a no-op cleanup; otherwise it writes
// a tagged copy to a fresh per-run temp dir under the OS cache dir and returns that copy
// with a cleanup that removes the temp dir.
//
// Self-containment trade-off: the timezone header is read three times across an upload (the
// batch precheck, here, and the post-rewrite verify); this keeps each step independently
// correct rather than threading state through. Note that when getVideoTimezoneExifFn is
// stubbed in tests the verify read is not exercised — the real verify is covered by the
// sandbox harness.
func prepareVideoForUpload(ctx context.Context, path string) (uploadPath string, cleanup func(), err error) {
	noop := func() {}

	results, err := getVideoTimezoneExifFn(ctx, []string{path})
	if err != nil {
		return "", noop, fmt.Errorf("failed to read video timezone metadata for %s: %w", path, err)
	}
	r, err := singleExifResult(results, path)
	if err != nil {
		return "", noop, err
	}

	hasCanon := r.DateTimeOriginal != "" && r.OffsetTimeOriginal != ""
	if hasCanon {
		switch {
		case hasExplicitTimezone(r.CreationDate) && creationDateMatchesCanon(r.CreationDate, r.DateTimeOriginal, r.OffsetTimeOriginal):
			// 2a: existing atom already matches Canon — upload the original untouched.
			return path, noop, nil
		case hasExplicitTimezone(r.CreationDate):
			// 2b: a timezone-bearing atom disagrees with Canon (likely the original bug).
			logger.Warn("Existing video creation date disagrees with Canon timezone; rewriting from Canon",
				slog.String("file", path),
				slog.String("existing_creation_date", r.CreationDate),
				slog.String("canon", r.DateTimeOriginal+r.OffsetTimeOriginal))
		default:
			// 2c: no usable atom — rewrite from Canon.
		}
		return rewriteVideoCreationDate(ctx, path, r.DateTimeOriginal, r.OffsetTimeOriginal)
	}

	// No Canon fields. Trust an existing explicit-tz atom (iPhone / non-Canon already
	// carrying the Apple atom); otherwise we cannot prepare the file safely. The batch
	// precheck rejects this case before we get here.
	if hasExplicitTimezone(r.CreationDate) {
		return path, noop, nil
	}
	return "", noop, fmt.Errorf("video %s has no Canon timezone fields and no explicit-timezone creation date", path)
}

// rewriteVideoCreationDate produces a tagged copy of path in a fresh per-run temp dir under
// the OS cache dir, with the Apple creationdate atom derived from Canon's
// DateTimeOriginal+OffsetTimeOriginal. The temp keeps the original basename so Google sees
// a byte-identical filename. The caller passes the already-read Canon fields (dto, oto) so
// the post-write verify can confirm the atom encodes exactly that wall-clock+offset. On any
// failure it removes the per-run temp dir.
func rewriteVideoCreationDate(ctx context.Context, path, dto, oto string) (uploadPath string, cleanup func(), err error) {
	noop := func() {}

	root, err := videoTimezoneTempRoot()
	if err != nil {
		return "", noop, err
	}
	tempDir, err := os.MkdirTemp(root, "upload-*")
	if err != nil {
		return "", noop, fmt.Errorf("failed to create temp dir for %s: %w", path, err)
	}
	temp := filepath.Join(tempDir, filepath.Base(path))

	exiftoolPath, err := exec.LookPath("exiftool")
	if err != nil {
		os.RemoveAll(tempDir)
		return "", noop, fmt.Errorf("exiftool not found in PATH: %w", err)
	}
	// Single pass: read DateTimeOriginal+OffsetTimeOriginal from the source and write a
	// tagged copy to temp. exiftool's own working temp lives inside tempDir (same fs as
	// temp), so nothing renames across the device boundary; only the source read crosses it.
	cmd := exec.CommandContext(ctx, exiftoolPath,
		"-Keys:CreationDate<${DateTimeOriginal}${OffsetTimeOriginal}", "-o", temp, path)
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		os.RemoveAll(tempDir)
		if ctx.Err() != nil {
			return "", noop, ctx.Err()
		}
		return "", noop, fmt.Errorf("failed to write creation date for %s: %w: %s", path, runErr, strings.TrimSpace(string(out)))
	}

	// Verify the rewrite produced an atom encoding exactly Canon's wall-clock+offset. This is
	// stricter than checking for "some" explicit timezone: it also catches a doubled or
	// garbled offset (e.g. if DateTimeOriginal had carried its own offset), which would fail
	// normalization and so not match.
	verify, err := getVideoTimezoneExifFn(ctx, []string{temp})
	if err != nil {
		os.RemoveAll(tempDir)
		return "", noop, fmt.Errorf("failed to verify creation date for %s: %w", path, err)
	}
	vr, err := singleExifResult(verify, temp)
	if err != nil {
		os.RemoveAll(tempDir)
		return "", noop, err
	}
	if !creationDateMatchesCanon(vr.CreationDate, dto, oto) {
		os.RemoveAll(tempDir)
		return "", noop, fmt.Errorf("creation date rewrite for %s did not match Canon timezone (got %q, want %q)", path, vr.CreationDate, dto+oto)
	}

	return temp, func() { os.RemoveAll(tempDir) }, nil
}

// singleExifResult requires exactly one result whose path matches want (compared with
// filepath.Clean), returning an error on a missing, duplicate, or unexpected result.
func singleExifResult(results []videoTimezoneExif, want string) (videoTimezoneExif, error) {
	wantClean := filepath.Clean(want)
	var match *videoTimezoneExif
	for i := range results {
		if filepath.Clean(results[i].Path) != wantClean {
			return videoTimezoneExif{}, fmt.Errorf("unexpected exiftool result for %s: got %s", want, results[i].Path)
		}
		if match != nil {
			return videoTimezoneExif{}, fmt.Errorf("duplicate exiftool result for %s", want)
		}
		match = &results[i]
	}
	if match == nil {
		return videoTimezoneExif{}, fmt.Errorf("no exiftool result for %s", want)
	}
	return *match, nil
}
