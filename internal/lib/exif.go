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
	Path                string // exiftool SourceFile, mirrors ExifData.Path
	DateTimeOriginal    string // Canon DateTimeOriginal, e.g. "2026:04:03 16:37:51"
	OffsetTimeOriginal  string // Canon OffsetTimeOriginal, e.g. "-08:00"
	CreationDate        string // com.apple.quicktime.creationdate (Keys:CreationDate)
	QuickTimeCreateDate string // mvhd QuickTime:CreateDate, e.g. "2026:04:04 00:37:52" (UTC on Canon)
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

	// QuickTimeUTC=0 pins the QuickTime date reads to the literal stored value rather than
	// UTC-converting them, mirroring the write in remuxAndTagCanonVideo. Without this, a
	// configured QuickTimeUTC=1 would skew QuickTime:CreateDate on read and make the post-remux
	// mvhd verify spuriously fail on every Canon video.
	args := []string{"-api", "QuickTimeUTC=0", "-j", "-DateTimeOriginal", "-OffsetTimeOriginal", "-Keys:CreationDate", "-QuickTime:CreateDate"}
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
		CreateDate         string `json:"CreateDate,omitempty"` // QuickTime:CreateDate (mvhd)
	}
	if err := json.Unmarshal(output, &results); err != nil {
		return nil, fmt.Errorf("failed to unmarshal exiftool output: %w", err)
	}

	exifData := make([]videoTimezoneExif, 0, len(results))
	for _, r := range results {
		exifData = append(exifData, videoTimezoneExif{
			Path:                r.SourceFile,
			DateTimeOriginal:    r.DateTimeOriginal,
			OffsetTimeOriginal:  r.OffsetTimeOriginal,
			CreationDate:        r.CreationDate,
			QuickTimeCreateDate: r.CreateDate,
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
// function the caller must defer. For Canon videos it remuxes a QuickTime .mov copy (see
// remuxAndTagCanonVideo) into a fresh per-run temp dir under the OS cache dir and returns
// that copy with a cleanup that removes the temp dir; for non-Canon videos that already
// carry an explicit-timezone atom (iPhone .mov, already a QuickTime container) it returns
// the original path with a no-op cleanup.
//
// Why Canon always needs the remux: Google Photos only honors the com.apple.quicktime.
// creationdate atom's offset when the file is a QuickTime-brand ('qt') container. Canon
// writes an MP4-brand ('mp42') container, for which Google mis-parses the atom — taking its
// local wall-clock as UTC and re-applying the offset, so the displayed time lands off by the
// offset. No metadata edit (atom, mvhd, EXIF) changes that; only the container does. See
// remuxAndTagCanonVideo and docs/google-photos-video-timezone.md.
//
// Self-containment trade-off: the timezone header is read three times across an upload (the
// batch precheck, here, and the post-remux verify); this keeps each step independently
// correct rather than threading state through. Note that when getVideoTimezoneExifFn is
// stubbed in tests the remux/verify is not exercised — the real path is covered by the
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
		// Warn if a pre-existing timezone-bearing atom disagrees with Canon (likely a file
		// left over from an earlier buggy attempt); we remux and re-tag from Canon regardless.
		if hasExplicitTimezone(r.CreationDate) && !creationDateMatchesCanon(r.CreationDate, r.DateTimeOriginal, r.OffsetTimeOriginal) {
			logger.Warn("Existing video creation date disagrees with Canon timezone; re-tagging from Canon",
				slog.String("file", path),
				slog.String("existing_creation_date", r.CreationDate),
				slog.String("canon", r.DateTimeOriginal+r.OffsetTimeOriginal))
		}
		return remuxAndTagCanonVideo(ctx, path, r.DateTimeOriginal, r.OffsetTimeOriginal)
	}

	// No Canon fields. Trust an existing explicit-tz atom (iPhone / non-Canon already carrying
	// the Apple atom in a QuickTime container); otherwise we cannot prepare the file safely.
	// The batch precheck rejects the latter case before we get here.
	if hasExplicitTimezone(r.CreationDate) {
		return path, noop, nil
	}
	return "", noop, fmt.Errorf("video %s has no Canon timezone fields and no explicit-timezone creation date", path)
}

// remuxAndTagCanonVideo produces an upload-ready copy of a Canon video in a fresh per-run
// temp dir under the OS cache dir, applying the timezone fix Google Photos actually honors.
//
// The fix is the CONTAINER, not metadata. Google decides whether the com.apple.quicktime.
// creationdate atom's wall-clock is local or UTC from the file's ftyp brand: a QuickTime
// ('qt') brand is trusted and the offset applied; an MP4 ('mp42') brand — what Canon writes —
// is mis-parsed (the atom's local time is taken as UTC and the offset re-applied), landing the
// displayed time off by the offset. Adding the atom, rewriting mvhd, or stripping EXIF do NOT
// change this; only the container does. Verified end-to-end (identical Canon content settles
// to the true instant as a .mov ('qt') and to local-as-Z as a .MP4 ('mp42')); see
// docs/google-photos-video-timezone.md.
//
// So we losslessly remux the Canon MP4 to a QuickTime .mov ('qt' brand) with ffmpeg -c copy
// (no re-encode), then add the creationdate atom (local wall-clock + offset) and restore the
// mvhd create/modify dates that ffmpeg zeroes (to the true UTC instant). The temp is named
// <stem>.mov; the caller presents that .mov name to Google. The queued original — the only
// surviving copy after the SD card is wiped — is never touched. On any failure the per-run
// temp dir is removed.
func remuxAndTagCanonVideo(ctx context.Context, path, dto, oto string) (uploadPath string, cleanup func(), err error) {
	noop := func() {}

	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", noop, fmt.Errorf("ffmpeg not found in PATH (needed to remux %s): %w", path, err)
	}
	exiftoolPath, err := exec.LookPath("exiftool")
	if err != nil {
		return "", noop, fmt.Errorf("exiftool not found in PATH: %w", err)
	}
	utc, ok := canonUTCInstant(dto, oto)
	if !ok {
		return "", noop, fmt.Errorf("could not compute UTC instant for %s from %q + %q", path, dto, oto)
	}

	root, err := videoTimezoneTempRoot()
	if err != nil {
		return "", noop, err
	}
	tempDir, err := os.MkdirTemp(root, "upload-*")
	if err != nil {
		return "", noop, fmt.Errorf("failed to create temp dir for %s: %w", path, err)
	}
	base := filepath.Base(path)
	temp := filepath.Join(tempDir, strings.TrimSuffix(base, filepath.Ext(base))+".mov")

	// Lossless remux MP4 -> QuickTime .mov ('qt' brand). -map 0 keeps every stream; -c copy
	// rewraps without re-encoding (near-instant, no quality loss). This assumes every source
	// stream is .mov-muxable: if a camera ever writes a stream the QuickTime muxer rejects,
	// ffmpeg errors here and the upload fails fast (verified clean on Canon R6 Mark II —
	// video + audio + timed-metadata track all copy without issue).
	remux := exec.CommandContext(ctx, ffmpegPath, "-nostdin", "-y", "-loglevel", "error",
		"-i", path, "-map", "0", "-c", "copy", temp)
	if out, runErr := remux.CombinedOutput(); runErr != nil {
		os.RemoveAll(tempDir)
		if ctx.Err() != nil {
			return "", noop, ctx.Err()
		}
		return "", noop, fmt.Errorf("failed to remux %s to .mov: %w: %s", path, runErr, strings.TrimSpace(string(out)))
	}

	// Add the Apple creationdate atom (local wall-clock + offset) and restore the mvhd dates to
	// the true UTC instant (ffmpeg zeroes them). QuickTimeUTC=0 makes exiftool store these
	// literally instead of applying its own UTC conversion.
	tag := exec.CommandContext(ctx, exiftoolPath, "-api", "QuickTimeUTC=0", "-overwrite_original",
		"-Keys:CreationDate="+dto+oto,
		"-QuickTime:CreateDate="+utc,
		"-QuickTime:ModifyDate="+utc,
		temp)
	if out, runErr := tag.CombinedOutput(); runErr != nil {
		os.RemoveAll(tempDir)
		if ctx.Err() != nil {
			return "", noop, ctx.Err()
		}
		return "", noop, fmt.Errorf("failed to tag %s: %w: %s", path, runErr, strings.TrimSpace(string(out)))
	}

	// Verify the result is exactly what Google needs: a QuickTime-brand container (the whole
	// point of the remux), the atom encoding exactly Canon's wall-clock+offset, and mvhd holding
	// the true UTC instant.
	brand, err := videoMajorBrand(ctx, temp)
	if err != nil {
		os.RemoveAll(tempDir)
		return "", noop, fmt.Errorf("failed to read major brand for %s: %w", path, err)
	}
	if !strings.Contains(brand, "QuickTime") {
		os.RemoveAll(tempDir)
		return "", noop, fmt.Errorf("remux of %s did not produce a QuickTime container (brand %q)", path, brand)
	}
	verify, err := getVideoTimezoneExifFn(ctx, []string{temp})
	if err != nil {
		os.RemoveAll(tempDir)
		return "", noop, fmt.Errorf("failed to verify %s: %w", path, err)
	}
	vr, err := singleExifResult(verify, temp)
	if err != nil {
		os.RemoveAll(tempDir)
		return "", noop, err
	}
	if !creationDateMatchesCanon(vr.CreationDate, dto, oto) {
		os.RemoveAll(tempDir)
		return "", noop, fmt.Errorf("creationdate tag for %s did not match Canon timezone (got %q, want %q)", path, vr.CreationDate, dto+oto)
	}
	if !sameWallClock(vr.QuickTimeCreateDate, utc) {
		os.RemoveAll(tempDir)
		return "", noop, fmt.Errorf("mvhd for %s was not set to the UTC instant (got %q, want %q)", path, vr.QuickTimeCreateDate, utc)
	}

	return temp, func() { os.RemoveAll(tempDir) }, nil
}

// canonUTCInstant converts Canon's local DateTimeOriginal + OffsetTimeOriginal to the UTC
// instant as an offset-less exiftool timestamp (e.g. "2026:04:04 00:37:51"), used to restore
// the mvhd create/modify dates that ffmpeg zeroes during the remux.
func canonUTCInstant(dto, oto string) (string, bool) {
	norm, ok := normalizeTimestampWithOffset(dto + oto)
	if !ok {
		return "", false
	}
	t, err := time.Parse("2006:01:02 15:04:05-07:00", norm)
	if err != nil {
		return "", false
	}
	return t.UTC().Format("2006:01:02 15:04:05"), true
}

// videoMajorBrand returns the file's ftyp MajorBrand as exiftool's descriptive string (e.g.
// "Apple QuickTime (.MOV/QT)" or "MP4 v2 [ISO 14496-14]"), used to confirm the remux produced
// a QuickTime container.
func videoMajorBrand(ctx context.Context, path string) (string, error) {
	exiftoolPath, err := exec.LookPath("exiftool")
	if err != nil {
		return "", fmt.Errorf("exiftool not found in PATH: %w", err)
	}
	out, err := exec.CommandContext(ctx, exiftoolPath, "-s", "-s", "-s", "-MajorBrand", path).Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("failed to read major brand: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// sameWallClock reports whether two exiftool timestamps without timezone (e.g.
// "2026:04:03 16:37:51") denote the same date-time at second precision. Sub-seconds are
// dropped, mirroring normalizeTimestampWithOffset. A value that fails to parse never matches.
func sameWallClock(a, b string) bool {
	const layout = "2006:01:02 15:04:05.999999999"
	pa, err := time.Parse(layout, strings.TrimSpace(a))
	if err != nil {
		return false
	}
	pb, err := time.Parse(layout, strings.TrimSpace(b))
	if err != nil {
		return false
	}
	return pa.Format("2006:01:02 15:04:05") == pb.Format("2006:01:02 15:04:05")
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
