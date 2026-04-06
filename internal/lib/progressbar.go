package lib

import (
	"fmt"

	"github.com/schollz/progressbar/v3"
)

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
		progressbar.OptionOnCompletion(func() { fmt.Println() }),
	)
}

func NewCountProgressBar(total int, description string) *progressbar.ProgressBar {
	return progressbar.NewOptions(total,
		progressbar.OptionSetDescription(description+":"),
		progressbar.OptionSetWidth(20), // Fit in an 80-column terminal.
		progressbar.OptionShowCount(),
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionShowElapsedTimeOnFinish(),
		progressbar.OptionOnCompletion(func() { fmt.Println() }),
	)
}
