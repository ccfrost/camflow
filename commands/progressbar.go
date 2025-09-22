package commands

import "github.com/schollz/progressbar/v3"

func NewProgressBar(size int64, description string) *progressbar.ProgressBar {
	return progressbar.NewOptions64(size,
		progressbar.OptionSetDescription(description+":"),
		progressbar.OptionSetWidth(20), // Fit in an 80-column terminal.
		progressbar.OptionShowBytes(true),
		progressbar.OptionUseIECUnits(true),
		progressbar.OptionShowCount(), // Show number of bytes moved.
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionShowTotalBytes(true),
		progressbar.OptionShowElapsedTimeOnFinish(),
	)
}
