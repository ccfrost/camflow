package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_EnvVars(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	configContent := `
photos_process_queue_root = "/tmp/photos"
[google_photos]
client_id = "file-client-id"
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	// Set environment variables to override config
	t.Setenv("CAMFLOW_GOOGLE_PHOTOS_CLIENT_ID", "env-client-id")
	t.Setenv("CAMFLOW_PHOTOS_PROCESS_QUEUE_ROOT", "/env/photos")

	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)

	// Check that Env vars take precedence
	assert.Equal(t, "env-client-id", cfg.GooglePhotos.ClientId, "Environment variable should override config file for nested struct")
	assert.Equal(t, "/env/photos", cfg.PhotosProcessQueueRoot, "Environment variable should override config file for top level field")
}

func TestParseOAuthLoopbackRedirectURI(t *testing.T) {
	valid := []string{
		"http://localhost:8080",
		"HTTP://LOCALHOST:8080/callback",
		"http://127.0.0.1:8080/oauth/callback",
		"http://[::1]:8080/callback",
	}
	for _, raw := range valid {
		t.Run("valid "+raw, func(t *testing.T) {
			u, err := ParseOAuthLoopbackRedirectURI(raw)
			require.NoError(t, err)
			assert.NotEmpty(t, u.Host)
		})
	}

	invalid := []struct {
		raw   string
		match string
	}{
		{raw: "", match: "http scheme"},
		{raw: "localhost:8080", match: "http scheme"},
		{raw: "https://localhost:8080", match: "http scheme"},
		{raw: "http://0.0.0.0:8080", match: "loopback"},
		{raw: "http://192.168.1.10:8080", match: "loopback"},
		{raw: "http://example.com:8080", match: "loopback"},
		{raw: "http://127.1.2.3:8080", match: "127.0.0.1"},
		{raw: "http://localhost", match: "explicit port"},
		{raw: "http://localhost:0", match: "1 through 65535"},
		{raw: "http://localhost:65536", match: "1 through 65535"},
		{raw: "http://localhost:http", match: "invalid port"},
		{raw: "http://user@localhost:8080", match: "user information"},
		{raw: "http://localhost:8080/callback?fixed=value", match: "query string"},
		{raw: "http://localhost:8080/callback?", match: "query string"},
		{raw: "http://localhost:8080/callback#fragment", match: "fragment"},
	}
	for _, tt := range invalid {
		t.Run("invalid "+tt.raw, func(t *testing.T) {
			_, err := ParseOAuthLoopbackRedirectURI(tt.raw)
			require.Error(t, err)
			assert.ErrorContains(t, err, "google_photos.redirect_uri")
			assert.ErrorContains(t, err, tt.match)
		})
	}
}

func TestGooglePhotosConfigValidateRedirectURI(t *testing.T) {
	base := func() GooglePhotosConfig {
		return GooglePhotosConfig{ClientId: "id", ClientSecret: "secret"}
	}

	t.Run("empty defaults to numeric IPv4 loopback", func(t *testing.T) {
		c := base()
		require.NoError(t, c.Validate())
		assert.Equal(t, "http://127.0.0.1:8080", c.RedirectURI)
	})

	t.Run("legacy OOB overridden with default", func(t *testing.T) {
		c := base()
		c.RedirectURI = "urn:ietf:wg:oauth:2.0:oob"
		require.NoError(t, c.Validate())
		assert.Equal(t, "http://127.0.0.1:8080", c.RedirectURI)
	})

	t.Run("valid loopback preserved", func(t *testing.T) {
		c := base()
		c.RedirectURI = "http://127.0.0.1:9999/callback"
		require.NoError(t, c.Validate())
		assert.Equal(t, "http://127.0.0.1:9999/callback", c.RedirectURI)
	})

	t.Run("malformed rejected at config load", func(t *testing.T) {
		c := base()
		c.RedirectURI = "http://0.0.0.0:8080"
		err := c.Validate()
		require.Error(t, err)
		assert.ErrorContains(t, err, "google_photos.redirect_uri")
		assert.ErrorContains(t, err, "loopback")
	})
}
