package lib

import (
	"context"
	"io"
	"net/http"
	"sync"

	"github.com/schollz/progressbar/v3"
)

// uploadEndpointPath is the path of the Google Photos raw-upload endpoint. It mirrors the
// defaultEndpoint constant in the gphotos uploader library
// (https://photoslibrary.googleapis.com/v1/uploads). We gate progress accounting on this so
// that only file-upload request bodies are counted, not album/media-item API calls. If the
// library ever changes this path, progress degrades gracefully to per-file jumps (see
// fileProgress.finish) rather than breaking.
const uploadEndpointPath = "/v1/uploads"

// fileProgress tracks how many bytes of a single file's upload have been credited to the
// progress bar. It is carried through the request context so that each in-flight upload
// reports into its own counter — this keeps the design correct if uploads are ever
// parallelized (the only shared object is the bar, whose Add64 is internally synchronized).
//
// Accounting is relative and capped: a file contributes exactly its declared size to the
// bar, no more. The cap prevents a retried (re-streamed) upload from overshooting, and the
// relative remainder credited by finish keeps totals exact regardless of completion order.
type fileProgress struct {
	bar  *progressbar.ProgressBar
	size int64

	mu    sync.Mutex
	added int64 // bytes already credited to the bar for this file (0 <= added <= size)
}

// add credits up to n more bytes to the bar, never exceeding the file's declared size.
func (fp *fileProgress) add(n int) {
	if fp == nil || n <= 0 {
		return
	}
	fp.mu.Lock()
	credit := min(int64(n), fp.size-fp.added)
	fp.added += credit
	fp.mu.Unlock()
	if credit > 0 {
		_ = fp.bar.Add64(credit)
	}
}

// finish credits any bytes not yet accounted for (e.g. the unstreamed remainder, or the
// whole file in dry-run / when no body bytes were observed) so the file contributes exactly
// its declared size to the bar.
func (fp *fileProgress) finish() {
	if fp == nil {
		return
	}
	fp.mu.Lock()
	remainder := fp.size - fp.added
	fp.added = fp.size
	fp.mu.Unlock()
	if remainder > 0 {
		_ = fp.bar.Add64(remainder)
	}
}

// progressContextKey is an unexported context key type for carrying a *fileProgress.
type progressContextKey struct{}

// contextWithFileProgress returns a context carrying fp so the upload progress transport can
// credit the request body's bytes to it.
func contextWithFileProgress(ctx context.Context, fp *fileProgress) context.Context {
	return context.WithValue(ctx, progressContextKey{}, fp)
}

// fileProgressFromContext returns the *fileProgress carried by ctx, if any.
func fileProgressFromContext(ctx context.Context) *fileProgress {
	fp, _ := ctx.Value(progressContextKey{}).(*fileProgress)
	return fp
}

// countingReadCloser wraps an upload request body, crediting bytes to fp as they are read
// (i.e. as they are written to the socket by the HTTP transport).
type countingReadCloser struct {
	rc io.ReadCloser
	fp *fileProgress
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	c.fp.add(n)
	return n, err
}

func (c *countingReadCloser) Close() error {
	return c.rc.Close()
}

// uploadProgressTransport is an http.RoundTripper that, for Google Photos upload requests
// carrying a *fileProgress in their context, wraps the request body so the progress bar
// advances as bytes are streamed to the wire.
//
// It is installed on the oauth2 *http.Client's Transport, i.e. below the retryablehttp retry
// layer, so it observes real on-the-wire reads (retryablehttp streams an *os.File lazily via
// its io.ReadSeeker path; it does not buffer the body). Only the body is wrapped — content
// length and headers are left untouched.
type uploadProgressTransport struct {
	base http.RoundTripper
}

// NewUploadProgressTransport wraps base so that upload request bodies are counted toward the
// progress bar. If base is nil, http.DefaultTransport is used.
func NewUploadProgressTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &uploadProgressTransport{base: base}
}

func (t *uploadProgressTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil && req.URL != nil && req.URL.Path == uploadEndpointPath {
		if fp := fileProgressFromContext(req.Context()); fp != nil {
			req.Body = &countingReadCloser{rc: req.Body, fp: fp}
		}
	}
	return t.base.RoundTrip(req)
}
