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

	// Verify that without the code change, it likely fails (or we just implement the fix directly)
	// But here we are writing the test that expects success *after* the change.
	
	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)

	// Check that Env vars take precedence
	assert.Equal(t, "env-client-id", cfg.GooglePhotos.ClientId, "Environment variable should override config file for nested struct")
	assert.Equal(t, "/env/photos", cfg.PhotosProcessQueueRoot, "Environment variable should override config file for top level field")
}
