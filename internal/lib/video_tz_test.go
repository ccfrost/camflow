package lib

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
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

	t.Run("no explicit tz and missing Canon fails", func(t *testing.T) {
		stubVideoTimezoneExif(t, func(_ context.Context, paths []string) ([]videoTimezoneExif, error) {
			return []videoTimezoneExif{{Path: paths[0], CreationDate: "2026:04:03 16:37:51"}}, nil
		})
		err := precheckVideoTimezones(context.Background(), items("/q/a.mp4"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot be prepared")
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
}

func TestPrepareVideoForUpload_DirectUploadWhenMatches(t *testing.T) {
	stubVideoTimezoneExif(t, func(_ context.Context, paths []string) ([]videoTimezoneExif, error) {
		return []videoTimezoneExif{{
			Path:               paths[0],
			DateTimeOriginal:   "2026:04:03 16:37:51",
			OffsetTimeOriginal: "-08:00",
			CreationDate:       "2026:04:03 16:37:51-08:00",
		}}, nil
	})

	path := "/queue/2026-04-03-IMG_3805.MP4"
	uploadPath, cleanup, err := prepareVideoForUpload(context.Background(), path)
	require.NoError(t, err)
	assert.Equal(t, path, uploadPath, "matching atom should upload the original directly")
	require.NotNil(t, cleanup)
	cleanup() // must be a safe no-op
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

	// We assert only that the rewrite branch was taken: a warning is logged and the original
	// is NOT returned for direct upload. Whether the real exiftool rewrite ultimately succeeds
	// (returning a temp path) or fails (returning "") is exercised by the sandbox harness, not
	// here — either way uploadPath differs from the original.
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
	tempPath := filepath.Join(t.TempDir(), "upload-xyz", basename)

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
	// Filename stays the original basename, even though we upload the temp.
	mockMediaItems.EXPECT().Create(gomock.Any(), media_items.SimpleMediaItem{UploadToken: "token", Filename: basename}).
		Return(&media_items.MediaItem{ID: "id", Filename: basename}, nil)

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
