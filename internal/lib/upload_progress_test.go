package lib

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/schollz/progressbar/v3"
)

// doUploadLikeRequest POSTs a body of the given size to path on srv through a client whose
// transport is the upload progress transport, carrying fp in the request context. It returns
// the bytes credited to fp's bar (CurrentNum).
func doUploadLikeRequest(t *testing.T, srv *httptest.Server, path string, size int64, fp *fileProgress) {
	t.Helper()
	client := &http.Client{Transport: NewUploadProgressTransport(nil)}
	ctx := contextWithFileProgress(context.Background(), fp)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+path, bytes.NewReader(make([]byte, size)))
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
}

// newSilentBar returns a real (visible) byte bar that renders to io.Discard. It must be
// visible because progressbar's Add64 is a no-op on an invisible bar, which would defeat the
// point of these tests.
func newSilentBar(size int64) *progressbar.ProgressBar {
	return progressbar.NewOptions64(size, progressbar.OptionSetWriter(io.Discard), progressbar.OptionShowBytes(true))
}

func TestUploadProgressTransport_CountsUploadBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // drain the streamed body
		_, _ = w.Write([]byte("token"))
	}))
	defer srv.Close()

	const size int64 = 1 << 20 // 1 MiB
	bar := newSilentBar(size)
	fp := &fileProgress{bar: bar, size: size}

	doUploadLikeRequest(t, srv, uploadEndpointPath, size, fp)

	if got := bar.State().CurrentNum; got != size {
		t.Errorf("bar credited %d bytes, want %d", got, size)
	}
}

func TestUploadProgressTransport_IgnoresNonUploadPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer srv.Close()

	const size int64 = 1 << 20
	bar := newSilentBar(size)
	fp := &fileProgress{bar: bar, size: size}

	doUploadLikeRequest(t, srv, "/v1/mediaItems:batchCreate", size, fp)

	if got := bar.State().CurrentNum; got != 0 {
		t.Errorf("non-upload request credited %d bytes, want 0", got)
	}
}

func TestFileProgress_CapsAtSizeAndFinishCreditsRemainder(t *testing.T) {
	const size int64 = 1000
	bar := newSilentBar(size)
	fp := &fileProgress{bar: bar, size: size}

	fp.add(600)
	fp.add(600) // would total 1200; must cap at size
	if got := bar.State().CurrentNum; got != size {
		t.Errorf("after over-adding, bar = %d, want capped at %d", got, size)
	}

	// A fresh file with no streamed bytes: finish must credit the whole size (dry-run path).
	bar2 := newSilentBar(size)
	fp2 := &fileProgress{bar: bar2, size: size}
	fp2.finish()
	if got := bar2.State().CurrentNum; got != size {
		t.Errorf("finish credited %d, want %d", got, size)
	}
}
