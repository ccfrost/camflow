package lib

import (
	"fmt"
	"time"

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
		// Rate-limit redraws: within-file progress credits bytes on every ~32 KiB socket
		// read, which would otherwise repaint far too often (and spam redirected stderr).
		progressbar.OptionThrottle(100*time.Millisecond),
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
