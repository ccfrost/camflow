package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDatePrefix(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectYear  string
		expectMonth string
		expectDay   string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Valid date with suffix",
			input:       "2024-12-25-Christmas-Video.mp4",
			expectYear:  "2024",
			expectMonth: "12",
			expectDay:   "25",
			expectError: false,
		},
		{
			name:        "Valid date minimal suffix",
			input:       "2023-01-01-file.jpg",
			expectYear:  "2023",
			expectMonth: "01",
			expectDay:   "01",
			expectError: false,
		},
		{
			name:        "Valid date with long suffix",
			input:       "2025-06-20-very-long-filename-with-many-parts.mov",
			expectYear:  "2025",
			expectMonth: "06",
			expectDay:   "20",
			expectError: false,
		},
		{
			name:        "Valid date exactly four parts",
			input:       "2024-12-31-file",
			expectYear:  "2024",
			expectMonth: "12",
			expectDay:   "31",
			expectError: false,
		},
		{
			name:        "Invalid - year too short",
			input:       "24-12-25-file.mp4",
			expectError: true,
			errorMsg:    "invalid format: year '24' must be 4 characters long",
		},
		{
			name:        "Invalid - year too long",
			input:       "20244-12-25-file.mp4",
			expectError: true,
			errorMsg:    "invalid format: year '20244' must be 4 characters long",
		},
		{
			name:        "Invalid - month too short",
			input:       "2024-1-25-file.mp4",
			expectError: true,
			errorMsg:    "invalid format: month '1' must be 2 characters long",
		},
		{
			name:        "Invalid - month too long",
			input:       "2024-123-25-file.mp4",
			expectError: true,
			errorMsg:    "invalid format: month '123' must be 2 characters long",
		},
		{
			name:        "Invalid - day too short",
			input:       "2024-12-5-file.mp4",
			expectError: true,
			errorMsg:    "invalid format: day '5' must be 2 characters long",
		},
		{
			name:        "Invalid - day too long",
			input:       "2024-12-255-file.mp4",
			expectError: true,
			errorMsg:    "invalid format: day '255' must be 2 characters long",
		},
		{
			name:        "Invalid - only three parts",
			input:       "2024-12-31",
			expectError: true,
			errorMsg:    "invalid format: expected at least 4 parts separated by '-'",
		},
		{
			name:        "Invalid - only two parts",
			input:       "2024-12",
			expectError: true,
			errorMsg:    "invalid format: expected at least 4 parts separated by '-'",
		},
		{
			name:        "Invalid - only one part",
			input:       "2024",
			expectError: true,
			errorMsg:    "invalid format: expected at least 4 parts separated by '-'",
		},
		{
			name:        "Invalid - no parts",
			input:       "",
			expectError: true,
			errorMsg:    "invalid format: expected at least 4 parts separated by '-'",
		},
		{
			name:        "Invalid - no dashes",
			input:       "20241225file.mp4",
			expectError: true,
			errorMsg:    "invalid format: expected at least 4 parts separated by '-'",
		},
		{
			name:        "Invalid - empty month",
			input:       "2024--25-file.mp4",
			expectError: true,
			errorMsg:    "invalid format: month '' must be 2 characters long",
		},
		{
			name:        "Invalid - empty year (leading dash)",
			input:       "-2024-12-25-file.mp4",
			expectError: true,
			errorMsg:    "invalid format: year '' must be 4 characters long",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			year, month, day, err := parseDatePrefix(tt.input)

			if tt.expectError {
				require.Error(t, err, "Expected an error for input: %s", tt.input)
				assert.Contains(t, err.Error(), tt.errorMsg, "Error message should contain expected text")
			} else {
				require.NoError(t, err, "Expected no error for input: %s", tt.input)
				assert.Equal(t, tt.expectYear, year, "Year should match")
				assert.Equal(t, tt.expectMonth, month, "Month should match")
				assert.Equal(t, tt.expectDay, day, "Day should match")
			}
		})
	}
}

func TestParseDatePrefix_RealWorldExamples(t *testing.T) {
	// Test with actual filename patterns that might be encountered
	realWorldCases := []struct {
		filename    string
		expectYear  string
		expectMonth string
		expectDay   string
	}{
		{"2024-01-28-camedia-test-IMG_4286-1.MP4", "2024", "01", "28"},
		{"2025-06-20-vacation-video.mov", "2025", "06", "20"},
		{"2023-12-31-new-years-eve-party.mp4", "2023", "12", "31"},
		{"2024-02-29-leap-year-video.avi", "2024", "02", "29"},
		{"2022-07-04-independence-day.mp4", "2022", "07", "04"},
	}

	for _, tc := range realWorldCases {
		t.Run(tc.filename, func(t *testing.T) {
			year, month, day, err := parseDatePrefix(tc.filename)
			require.NoError(t, err)
			assert.Equal(t, tc.expectYear, year)
			assert.Equal(t, tc.expectMonth, month)
			assert.Equal(t, tc.expectDay, day)
		})
	}
}

func TestParseDatePrefix_InvalidRealWorldExamples(t *testing.T) {
	// Test with real-world filename patterns that should fail validation
	invalidCases := []struct {
		filename string
		errorMsg string
	}{
		{"2022-7-4-independence-day.mp4", "invalid format: month '7' must be 2 characters long"},
		{"2023-1-15-new-year.mp4", "invalid format: month '1' must be 2 characters long"},
		{"2024-12-5-christmas.mp4", "invalid format: day '5' must be 2 characters long"},
		{"22-12-25-short-year.mp4", "invalid format: year '22' must be 4 characters long"},
		{"2024-123-01-long-month.mp4", "invalid format: month '123' must be 2 characters long"},
	}

	for _, tc := range invalidCases {
		t.Run(tc.filename, func(t *testing.T) {
			_, _, _, err := parseDatePrefix(tc.filename)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errorMsg)
		})
	}
}
