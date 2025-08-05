package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
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
