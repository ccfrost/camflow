package lib

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/gphotosuploader/google-photos-api-client-go/v3/albums"
	"github.com/gphotosuploader/google-photos-api-client-go/v3/media_items"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureLogger swaps the package logger for one writing to a buffer (at Debug level),
// returning the buffer and restoring the original via t.Cleanup.
func captureLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := logger
	logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { logger = orig })
	return &buf
}

// stubVideoTimezoneExif overrides getVideoTimezoneExifFn with the provided function,
// restoring the original via t.Cleanup.
func stubVideoTimezoneExif(t *testing.T, fn func(context.Context, []string) ([]videoTimezoneExif, error)) {
	t.Helper()
	orig := getVideoTimezoneExifFn
	getVideoTimezoneExifFn = fn
	t.Cleanup(func() { getVideoTimezoneExifFn = orig })
}

// stubPrepareVideoForUpload overrides prepareVideoForUploadFn, restoring it via t.Cleanup.
func stubPrepareVideoForUpload(t *testing.T, fn func(context.Context, string) (string, func(), error)) {
	t.Helper()
	orig := prepareVideoForUploadFn
	prepareVideoForUploadFn = fn
	t.Cleanup(func() { prepareVideoForUploadFn = orig })
}

func TestVideoTimezoneTempRoot(t *testing.T) {
	root, err := videoTimezoneTempRoot()
	require.NoError(t, err)

	cacheDir, err := os.UserCacheDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cacheDir, "camflow", "video-tz-tmp"), root)

	info, err := os.Stat(root)
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "expected temp root to be a directory")
}

func TestHasExplicitTimezone(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"2026:04:03 16:37:51-08:00", true},
		{"2026:04:03 16:37:51+02:00", true},
		{"2026:04:03 16:37:51-0800", true},
		{"2026:04:03 00:37:51+0000", true},
		{"2026:04:03 00:37:51Z", true},
		{"2026:04:03 16:37:51", false},
		{"", false},
		{"   ", false},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, hasExplicitTimezone(c.value), "hasExplicitTimezone(%q)", c.value)
	}
}

func TestCreationDateMatchesCanon(t *testing.T) {
	// Equal local wall-clock and offset → match.
	assert.True(t, creationDateMatchesCanon(
		"2026:04:03 16:37:51-08:00", "2026:04:03 16:37:51", "-08:00"))

	// Same UTC instant but different offset (Z vs -08:00) → NOT a match.
	assert.False(t, creationDateMatchesCanon(
		"2026:04:04 00:37:51Z", "2026:04:03 16:37:51", "-08:00"))

	// Different local time → no match.
	assert.False(t, creationDateMatchesCanon(
		"2026:04:03 17:37:51-08:00", "2026:04:03 16:37:51", "-08:00"))

	// CreationDate without explicit offset → no match.
	assert.False(t, creationDateMatchesCanon(
		"2026:04:03 16:37:51", "2026:04:03 16:37:51", "-08:00"))

	// Colon-less CreationDate offset is canonicalized, so it matches a colon-bearing canon.
	assert.True(t, creationDateMatchesCanon(
		"2026:04:03 16:37:51-0800", "2026:04:03 16:37:51", "-08:00"))

	// Sub-seconds on the CreationDate are ignored: same wall-clock + offset → match,
	// rather than a spurious mismatch that would trigger an unnecessary rewrite.
	assert.True(t, creationDateMatchesCanon(
		"2026:04:03 16:37:51.50-08:00", "2026:04:03 16:37:51", "-08:00"))

	// Sub-seconds are truncated, not rounded: a CreationDate ~0.01s below the next whole
	// second does NOT match a canon time on that second, even though they are nearly equal.
	assert.False(t, creationDateMatchesCanon(
		"2026:04:03 16:37:51.99-08:00", "2026:04:03 16:37:52", "-08:00"))
}

func TestCleanupOrphanedVideoTimezoneTempFiles(t *testing.T) {
	t.Run("removes upload-* dirs, keeps others", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "upload-111"), 0700))
		require.NoError(t, os.MkdirAll(filepath.Join(root, "upload-222"), 0700))
		require.NoError(t, os.MkdirAll(filepath.Join(root, "keepme"), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(root, "afile"), []byte("x"), 0600))

		require.NoError(t, cleanupOrphanedVideoTimezoneTempFiles(root, false))

		assertDirNotExists(t, filepath.Join(root, "upload-111"), "upload-111 should be removed")
		assertDirNotExists(t, filepath.Join(root, "upload-222"), "upload-222 should be removed")
		assertDirExists(t, filepath.Join(root, "keepme"), "non-upload dir should be kept")
		_, err := os.Stat(filepath.Join(root, "afile"))
		assert.NoError(t, err, "non-dir entry should be kept")
	})

	t.Run("dry-run removes nothing", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "upload-111"), 0700))

		require.NoError(t, cleanupOrphanedVideoTimezoneTempFiles(root, true))

		assertDirExists(t, filepath.Join(root, "upload-111"), "dry-run should not remove")
	})

	t.Run("missing root is a no-op", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "does-not-exist")
		assert.NoError(t, cleanupOrphanedVideoTimezoneTempFiles(root, false))
	})
}

func TestPrecheckVideoTimezones(t *testing.T) {
	items := func(paths ...string) []itemFileInfo {
		out := make([]itemFileInfo, len(paths))
		for i, p := range paths {
			out[i] = itemFileInfo{path: p}
		}
		return out
	}

	t.Run("explicit-tz creation date with no Canon is allowed", func(t *testing.T) {
		stubVideoTimezoneExif(t, func(_ context.Context, paths []string) ([]videoTimezoneExif, error) {
			return []videoTimezoneExif{{Path: paths[0], CreationDate: "2026:04:03 16:37:51-08:00"}}, nil
		})
		assert.NoError(t, precheckVideoTimezones(context.Background(), items("/q/a.mp4")))
	})

	t.Run("Canon fields with no creation date is allowed", func(t *testing.T) {
		stubVideoTimezoneExif(t, func(_ context.Context, paths []string) ([]videoTimezoneExif, error) {
			return []videoTimezoneExif{{Path: paths[0], DateTimeOriginal: "2026:04:03 16:37:51", OffsetTimeOriginal: "-08:00"}}, nil
		})
		assert.NoError(t, precheckVideoTimezones(context.Background(), items("/q/a.mp4")))
	})

	t.Run("no explicit tz and missing Canon is allowed (skipped per-item later)", func(t *testing.T) {
		stubVideoTimezoneExif(t, func(_ context.Context, paths []string) ([]videoTimezoneExif, error) {
			return []videoTimezoneExif{{Path: paths[0], CreationDate: "2026:04:03 16:37:51"}}, nil
		})
		// The precheck no longer rejects an un-prepareable video; it is skipped per-item at upload
		// time (prepareVideoForUpload errors, uploadMediaItem leaves it queued), so one such file
		// does not abort the batch.
		assert.NoError(t, precheckVideoTimezones(context.Background(), items("/q/a.mp4")))
	})

	t.Run("duplicate result fails", func(t *testing.T) {
		stubVideoTimezoneExif(t, func(_ context.Context, paths []string) ([]videoTimezoneExif, error) {
			return []videoTimezoneExif{
				{Path: paths[0], CreationDate: "2026:04:03 16:37:51-08:00"},
				{Path: paths[0], CreationDate: "2026:04:03 16:37:51-08:00"},
			}, nil
		})
		err := precheckVideoTimezones(context.Background(), items("/q/a.mp4"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate")
	})

	t.Run("missing result fails", func(t *testing.T) {
		stubVideoTimezoneExif(t, func(_ context.Context, _ []string) ([]videoTimezoneExif, error) {
			return []videoTimezoneExif{{Path: "/q/a.mp4", CreationDate: "2026:04:03 16:37:51-08:00"}}, nil
		})
		err := precheckVideoTimezones(context.Background(), items("/q/a.mp4", "/q/b.mp4"))
		require.Error(t, err)
	})

	t.Run("non-video files are filtered out of the batch", func(t *testing.T) {
		stubVideoTimezoneExif(t, func(_ context.Context, paths []string) ([]videoTimezoneExif, error) {
			// The stray non-video files must never reach exiftool; only the video is prechecked.
			assert.Equal(t, []string{"/q/a.mp4"}, paths)
			out := make([]videoTimezoneExif, len(paths))
			for i, p := range paths {
				out[i] = videoTimezoneExif{Path: p, CreationDate: "2026:04:03 16:37:51-08:00"}
			}
			return out, nil
		})
		assert.NoError(t, precheckVideoTimezones(context.Background(), items("/q/a.mp4", "/q/sidecar.xmp", "/q/note.txt")))
	})
}

func TestPrepareVideoForUpload_DirectUploadWhenNonCanonExplicitTz(t *testing.T) {
	// No Canon fields, but an explicit-timezone atom (iPhone / non-Canon, whose mvhd Apple
	// already writes in local time) → upload the original directly, no rewrite.
	stubVideoTimezoneExif(t, func(_ context.Context, paths []string) ([]videoTimezoneExif, error) {
		return []videoTimezoneExif{{
			Path:         paths[0],
			CreationDate: "2026:04:03 16:37:51-08:00",
		}}, nil
	})

	path := "/queue/IMG_iphone.MOV"
	uploadPath, cleanup, err := prepareVideoForUpload(context.Background(), path)
	require.NoError(t, err)
	assert.Equal(t, path, uploadPath, "non-Canon explicit-tz atom should upload the original directly")
	require.NotNil(t, cleanup)
	cleanup() // must be a safe no-op
}

func TestPrepareVideoForUpload_CanonAlwaysRewrites(t *testing.T) {
	// Even when the atom already matches Canon, a Canon file must still get a .mov copy: a .MP4
	// filename is mis-parsed by Google regardless of the atom. So the original must NOT be
	// uploaded directly.
	stubVideoTimezoneExif(t, func(_ context.Context, paths []string) ([]videoTimezoneExif, error) {
		return []videoTimezoneExif{{
			Path:                paths[0],
			DateTimeOriginal:    "2026:04:03 16:37:51",
			OffsetTimeOriginal:  "-08:00",
			CreationDate:        "2026:04:03 16:37:51-08:00", // already-correct atom
			QuickTimeCreateDate: "2026:04:04 00:37:52",       // but still an mp42 container
		}}, nil
	})

	// exiftool tagging this fake mp4 will fail; we assert only that the copy-and-tag branch was
	// taken (uploadPath differs from the original), as in the disagree test.
	path := filepath.Join(t.TempDir(), "2026-04-03-IMG_3805.MP4")
	require.NoError(t, os.WriteFile(path, []byte("not a real mp4"), 0644))

	uploadPath, cleanup, _ := prepareVideoForUpload(context.Background(), path)
	if cleanup != nil {
		defer cleanup()
	}
	assert.NotEqual(t, path, uploadPath, "Canon file must get a .mov copy, not be uploaded directly")
}

func TestCanonUTCInstant(t *testing.T) {
	utc, ok := canonUTCInstant("2026:04:03 16:37:51", "-08:00")
	require.True(t, ok)
	assert.Equal(t, "2026:04:04 00:37:51", utc) // 16:37:51 -08:00 -> next-day 00:37:51 UTC

	utc, ok = canonUTCInstant("2026:06:06 07:55:00", "-08:00")
	require.True(t, ok)
	assert.Equal(t, "2026:06:06 15:55:00", utc)

	// Colon-less offset is accepted (normalized before parsing).
	utc, ok = canonUTCInstant("2026:04:03 16:37:51", "-0800")
	require.True(t, ok)
	assert.Equal(t, "2026:04:04 00:37:51", utc)

	// Missing/garbled offset cannot be converted.
	_, ok = canonUTCInstant("2026:04:03 16:37:51", "")
	assert.False(t, ok)
}

func TestSameWallClock(t *testing.T) {
	assert.True(t, sameWallClock("2026:04:03 16:37:51", "2026:04:03 16:37:51"))
	// Sub-seconds are dropped before comparing.
	assert.True(t, sameWallClock("2026:04:03 16:37:51.70", "2026:04:03 16:37:51"))
	// Canon's UTC mvhd must NOT count as the local wall-clock.
	assert.False(t, sameWallClock("2026:04:04 00:37:52", "2026:04:03 16:37:51"))
	// Unparseable input never matches.
	assert.False(t, sameWallClock("", "2026:04:03 16:37:51"))
	assert.False(t, sameWallClock("2026:04:03 16:37:51-08:00", "2026:04:03 16:37:51"))
}

// TestCopyAndTagCanonVideo_HappyPath exercises the real single-pass exiftool -o copy-and-tag +
// verify (no stubs) on a generated video, locking in the exiftool arg strings and the mvhd set.
// It skips when ffmpeg/exiftool are unavailable (ffmpeg only generates the fixture; the function
// itself no longer needs it) so the unit suite still runs without them.
func TestCopyAndTagCanonVideo_HappyPath(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "exiftool"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found in PATH; skipping happy-path copy-and-tag test", tool)
		}
	}

	// A tiny real MP4, encoded with the always-built-in mpeg4 codec (no libx264 / no audio needed).
	src := filepath.Join(t.TempDir(), "2026-04-03-IMG_TEST.MP4")
	gen := exec.Command("ffmpeg", "-nostdin", "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=1:size=128x128:rate=10",
		"-c:v", "mpeg4", "-pix_fmt", "yuv420p", "-an", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("failed to generate test mp4: %v\n%s", err, out)
	}

	srcInfoBefore, err := os.Stat(src)
	require.NoError(t, err)

	const dto, oto = "2026:04:03 16:37:51", "-08:00"
	uploadPath, cleanup, err := copyAndTagCanonVideo(context.Background(), src, dto, oto)
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	defer cleanup()

	// The upload copy is named <stem>.mov — the whole fix is the filename extension.
	assert.Equal(t, ".mov", filepath.Ext(uploadPath))

	// The creationdate atom carries Canon's wall-clock+offset; mvhd create/modify hold the UTC instant.
	res, err := getVideoTimezoneExif(context.Background(), []string{uploadPath})
	require.NoError(t, err)
	r, err := singleExifResult(res, uploadPath)
	require.NoError(t, err)
	assert.Truef(t, creationDateMatchesCanon(r.CreationDate, dto, oto),
		"creationdate %q should match Canon %s%s", r.CreationDate, dto, oto)
	utc, ok := canonUTCInstant(dto, oto)
	require.True(t, ok)
	assert.Truef(t, sameWallClock(r.QuickTimeCreateDate, utc), "mvhd CreateDate %q should be UTC %q", r.QuickTimeCreateDate, utc)
	assert.Truef(t, sameWallClock(r.QuickTimeModifyDate, utc), "mvhd ModifyDate %q should be UTC %q", r.QuickTimeModifyDate, utc)

	// The queued original is never touched — only the copy is tagged, so the source still
	// carries no creationdate atom.
	origRes, err := getVideoTimezoneExif(context.Background(), []string{src})
	require.NoError(t, err)
	origR, err := singleExifResult(origRes, src)
	require.NoError(t, err)
	assert.Empty(t, origR.CreationDate, "the original must not be tagged (only the .mov copy is)")

	// Stronger than the atom check above: -o opens the source read-only, so its size and mtime
	// must be byte-for-byte unchanged. This locks in copyAndTagCanonVideo's own post-tag guard.
	srcInfoAfter, err := os.Stat(src)
	require.NoError(t, err)
	assert.Equal(t, srcInfoBefore.Size(), srcInfoAfter.Size(), "original size must not change")
	assert.True(t, srcInfoBefore.ModTime().Equal(srcInfoAfter.ModTime()), "original mtime must not change")

	// cleanup removes the per-run temp dir.
	cleanup()
	assertDirNotExists(t, filepath.Dir(uploadPath), "cleanup should remove the temp dir")
}

func TestPrepareVideoForUpload_DisagreeWarnsAndRewrites(t *testing.T) {
	buf := captureLogger(t)

	// CreationDate shares Canon's instant but carries Z instead of -08:00 → disagrees.
	stubVideoTimezoneExif(t, func(_ context.Context, paths []string) ([]videoTimezoneExif, error) {
		return []videoTimezoneExif{{
			Path:               paths[0],
			DateTimeOriginal:   "2026:04:03 16:37:51",
			OffsetTimeOriginal: "-08:00",
			CreationDate:       "2026:04:04 00:37:51Z",
		}}, nil
	})

	// We assert only that the copy-and-tag branch was taken: a warning is logged and the original
	// is NOT returned for direct upload. Whether the real tag ultimately succeeds (returning a
	// temp .mov path) or fails (returning "") is exercised by the sandbox harness, not here —
	// either way uploadPath differs from the original.
	path := filepath.Join(t.TempDir(), "2026-04-03-IMG_3805.MP4")
	require.NoError(t, os.WriteFile(path, []byte("not a real mp4"), 0644))

	uploadPath, cleanup, _ := prepareVideoForUpload(context.Background(), path)
	if cleanup != nil {
		defer cleanup()
	}
	assert.NotEqual(t, path, uploadPath, "disagreeing atom must not be uploaded directly")
	assert.Contains(t, buf.String(), "disagrees with Canon timezone", "should warn before rewriting")
}

// --- Cleanup-path upload tests ---

func TestUploadVideos_PreparedTempPath_CleanupOnSuccess(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig(t, "", "")
	basename := "2026-04-03-IMG_3805.MP4"
	createTestFiles(t, cfg.VideosUploadQueueRoot, map[string]string{basename: "content"})
	originalPath := filepath.Join(cfg.VideosUploadQueueRoot, basename)
	// The upload copy is named <stem>.mov; Google sees that name.
	movBasename := "2026-04-03-IMG_3805.mov"
	tempPath := filepath.Join(t.TempDir(), "upload-xyz", movBasename)

	// Precheck passes; prepare returns a distinct temp path + a cleanup sentinel.
	stubVideoTimezoneExif(t, func(_ context.Context, paths []string) ([]videoTimezoneExif, error) {
		out := make([]videoTimezoneExif, len(paths))
		for i, p := range paths {
			out[i] = videoTimezoneExif{Path: p, CreationDate: "2026:04:03 16:37:51-08:00"}
		}
		return out, nil
	})
	cleanupCalled := false
	stubPrepareVideoForUpload(t, func(_ context.Context, p string) (string, func(), error) {
		assert.Equal(t, originalPath, p, "prepare should receive the original queue path")
		return tempPath, func() { cleanupCalled = true }, nil
	})

	ctrl := gomock.NewController(t)
	mockClient := NewMockGPhotosClient(ctrl)
	mockUploader := NewMockMediaUploader(ctrl)
	mockMediaItems := NewMockAppMediaItemsService(ctrl)
	mockClient.EXPECT().Uploader().Return(mockUploader).AnyTimes()
	mockClient.EXPECT().MediaItems().Return(mockMediaItems).AnyTimes()

	var capturedUploadPath string
	mockUploader.EXPECT().UploadFile(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, p string) (string, error) {
			capturedUploadPath = p
			return "token", nil
		})
	// Filename follows the uploaded (temp .mov) basename, not the original .MP4.
	mockMediaItems.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: "token", Filename: movBasename}).
		Return(&media_items.MediaItem{ID: "id", Filename: movBasename}, nil)

	require.NoError(t, UploadVideos(ctx, cfg, t.TempDir(), false, mockClient, false))

	assert.Equal(t, tempPath, capturedUploadPath, "UploadFile must receive the temp path")
	assert.True(t, cleanupCalled, "cleanup must run on success")

	// The original was moved to uploaded; the temp path is the prepare stub's concern.
	_, statErr := os.Stat(originalPath)
	assert.True(t, os.IsNotExist(statErr), "original should be moved out of the queue")
	year, month, day, err := parseDatePrefix(basename)
	require.NoError(t, err)
	_, statErr = os.Stat(filepath.Join(cfg.VideosUploadedRoot, year, month, day, basename))
	assert.NoError(t, statErr, "original should land in uploaded/")
}

func TestUploadVideos_PreparedTempPath_CleanupOnUploadError(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig(t, "", "")
	basename := "2026-04-03-IMG_3805.MP4"
	createTestFiles(t, cfg.VideosUploadQueueRoot, map[string]string{basename: "content"})
	originalPath := filepath.Join(cfg.VideosUploadQueueRoot, basename)

	stubVideoTimezoneExif(t, func(_ context.Context, paths []string) ([]videoTimezoneExif, error) {
		return []videoTimezoneExif{{Path: paths[0], CreationDate: "2026:04:03 16:37:51-08:00"}}, nil
	})
	cleanupCalled := false
	stubPrepareVideoForUpload(t, func(_ context.Context, _ string) (string, func(), error) {
		return "/tmp/upload-xyz/" + basename, func() { cleanupCalled = true }, nil
	})

	ctrl := gomock.NewController(t)
	mockClient := NewMockGPhotosClient(ctrl)
	mockUploader := NewMockMediaUploader(ctrl)
	mockClient.EXPECT().Uploader().Return(mockUploader).AnyTimes()
	mockUploader.EXPECT().UploadFile(gomock.Any(), gomock.Any()).Return("", errors.New("boom"))

	err := UploadVideos(ctx, cfg, t.TempDir(), false, mockClient, false)
	require.Error(t, err)
	assert.True(t, cleanupCalled, "cleanup must run even when upload fails")

	_, statErr := os.Stat(originalPath)
	assert.NoError(t, statErr, "original must remain in the queue when upload fails")
}

func TestUploadVideos_PreparedTempPath_CleanupOnAddToAlbumError(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig(t, "", "Videos Album")
	basename := "2026-04-03-IMG_3805.MP4"
	createTestFiles(t, cfg.VideosUploadQueueRoot, map[string]string{basename: "content"})

	stubVideoTimezoneExif(t, func(_ context.Context, paths []string) ([]videoTimezoneExif, error) {
		return []videoTimezoneExif{{Path: paths[0], CreationDate: "2026:04:03 16:37:51-08:00"}}, nil
	})
	cleanupCalled := false
	stubPrepareVideoForUpload(t, func(_ context.Context, _ string) (string, func(), error) {
		return "/tmp/upload-xyz/" + basename, func() { cleanupCalled = true }, nil
	})

	ctrl := gomock.NewController(t)
	mockClient := NewMockGPhotosClient(ctrl)
	mockUploader := NewMockMediaUploader(ctrl)
	mockMediaItems := NewMockAppMediaItemsService(ctrl)
	mockAlbums := NewMockAppAlbumsService(ctrl)
	mockClient.EXPECT().Uploader().Return(mockUploader).AnyTimes()
	mockClient.EXPECT().MediaItems().Return(mockMediaItems).AnyTimes()
	mockClient.EXPECT().Albums().Return(mockAlbums).AnyTimes()

	mockAlbums.EXPECT().List(gomock.Any()).Return([]albums.Album{}, nil).AnyTimes()
	mockAlbums.EXPECT().Create(gomock.Any(), "Videos Album").
		Return(&albums.Album{ID: "album-id", Title: "Videos Album"}, nil).AnyTimes()
	mockUploader.EXPECT().UploadFile(gomock.Any(), gomock.Any()).Return("token", nil)
	mockMediaItems.EXPECT().Create(gomock.Any(), gomock.Any()).
		Return(&media_items.MediaItem{ID: "id", Filename: basename}, nil)
	mockAlbums.EXPECT().AddMediaItems(gomock.Any(), gomock.Any(), gomock.Any()).Return(errors.New("boom"))

	err := UploadVideos(ctx, cfg, t.TempDir(), false, mockClient, false)
	require.Error(t, err)
	assert.True(t, cleanupCalled, "cleanup must run even when adding to album fails")
}

func TestUploadVideos_PrepareError_SkipsAndContinues(t *testing.T) {
	// A prepare failure on one video (e.g. exiftool cannot tag the copy) must skip just that
	// file — leaving it queued for a later run — and let the batch continue, not abort.
	ctx := context.Background()
	cfg := newTestConfig(t, "", "")
	badBase := "2026-04-03-IMG_0001.MP4"
	goodBase := "2026-04-03-IMG_0002.MP4"
	createTestFiles(t, cfg.VideosUploadQueueRoot, map[string]string{badBase: "content", goodBase: "content"})
	badPath := filepath.Join(cfg.VideosUploadQueueRoot, badBase)
	goodMov := "2026-04-03-IMG_0002.mov"
	goodTemp := filepath.Join(t.TempDir(), "upload-good", goodMov)

	stubVideoTimezoneExif(t, func(_ context.Context, paths []string) ([]videoTimezoneExif, error) {
		out := make([]videoTimezoneExif, len(paths))
		for i, p := range paths {
			out[i] = videoTimezoneExif{Path: p, CreationDate: "2026:04:03 16:37:51-08:00"}
		}
		return out, nil
	})
	goodCleanup := false
	stubPrepareVideoForUpload(t, func(_ context.Context, p string) (string, func(), error) {
		if filepath.Base(p) == badBase {
			return "", func() {}, errors.New("prepare failed")
		}
		return goodTemp, func() { goodCleanup = true }, nil
	})

	ctrl := gomock.NewController(t)
	mockClient := NewMockGPhotosClient(ctrl)
	mockUploader := NewMockMediaUploader(ctrl)
	mockMediaItems := NewMockAppMediaItemsService(ctrl)
	mockClient.EXPECT().Uploader().Return(mockUploader).AnyTimes()
	mockClient.EXPECT().MediaItems().Return(mockMediaItems).AnyTimes()

	// Only the good file reaches upload; the skipped one never calls UploadFile/Create.
	var uploaded []string
	mockUploader.EXPECT().UploadFile(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, p string) (string, error) {
			uploaded = append(uploaded, p)
			return "token", nil
		})
	mockMediaItems.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: "token", Filename: goodMov}).
		Return(&media_items.MediaItem{ID: "id", Filename: goodMov}, nil)

	require.NoError(t, UploadVideos(ctx, cfg, t.TempDir(), false, mockClient, false),
		"a prepare failure on one video must not fail the batch")

	assert.Equal(t, []string{goodTemp}, uploaded, "only the good video is uploaded")
	assert.True(t, goodCleanup, "good video cleanup runs")

	_, statErr := os.Stat(badPath)
	assert.NoError(t, statErr, "skipped video must remain in the queue for a later run")
	year, month, day, err := parseDatePrefix(goodBase)
	require.NoError(t, err)
	_, statErr = os.Stat(filepath.Join(cfg.VideosUploadedRoot, year, month, day, goodBase))
	assert.NoError(t, statErr, "uploaded video should land in uploaded/")
}

func TestUploadVideos_DryRun_DeletesNoTempsAndLogs(t *testing.T) {
	ctx := context.Background()
	cfg := newTestConfig(t, "", "")
	basename := "2026-04-03-IMG_3805.MP4"
	createTestFiles(t, cfg.VideosUploadQueueRoot, map[string]string{basename: "content"})

	prepareCalled := false
	stubVideoTimezoneExif(t, func(_ context.Context, paths []string) ([]videoTimezoneExif, error) {
		return []videoTimezoneExif{{Path: paths[0], CreationDate: "2026:04:03 16:37:51-08:00"}}, nil
	})
	stubPrepareVideoForUpload(t, func(_ context.Context, p string) (string, func(), error) {
		prepareCalled = true
		return p, func() {}, nil
	})

	ctrl := gomock.NewController(t)
	mockClient := NewMockGPhotosClient(ctrl)
	// No Uploader/MediaItems expectations: dry-run must not call them.

	require.NoError(t, UploadVideos(ctx, cfg, t.TempDir(), false, mockClient, true /* dryRun */))

	assert.False(t, prepareCalled, "dry-run must not prepare (rewrite) videos")
	// Original stays in the queue under dry-run.
	_, statErr := os.Stat(filepath.Join(cfg.VideosUploadQueueRoot, basename))
	assert.NoError(t, statErr, "dry-run must not move the original")
}
