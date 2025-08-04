package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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
