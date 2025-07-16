package camflowconfig

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

type KeyAlbum struct {
	Key   string `mapstructure:"key"`
	Album string `mapstructure:"album"`
}

// GooglePhotosConfig defines the configuration specific to Google Photos.
type GooglePhotosConfig struct {
	ClientId     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`
	RedirectURI  string `mapstructure:"redirect_uri"`

	Photos GPPhotosConfig `mapstructure:"photos"`
	Videos GPVideosConfig `mapstructure:"videos"`
}

// GPPhotosConfig defines the configuration for Photos in Google Photos.
type GPPhotosConfig struct {
	DefaultAlbum string `mapstructure:"default_album"`

	LabelAlbums   []KeyAlbum `mapstructure:"label_albums"`
	SubjectAlbums []KeyAlbum `mapstructure:"subject_albums"`
}

func (c *GPPhotosConfig) GetDefaultAlbum() string {
	return c.DefaultAlbum
}

// GPVideosConfig defines the configuration for Videos in Google Photos.
type GPVideosConfig struct {
	DefaultAlbum string `mapstructure:"default_album"`
}

func (c *GPVideosConfig) GetDefaultAlbum() string {
	return c.DefaultAlbum
}

// TODO: rename to camflow.
// CamflowConfig defines the configuration for Camflow.
// TODO: move flat fields into the new structs.
type CamflowConfig struct {
	PhotosToProcessRoot  string            `mapstructure:"photos_to_process_root"`
	PhotosExportQueueDir string            `mapstructure:"photos_export_queue_dir"`
	PhotosExportedRoot   string            `mapstructure:"photos_exported_root"`
	LocalPhotos          LocalPhotosConfig `mapstructure:"-"`

	VideosExportQueueRoot string            `mapstructure:"videos_export_queue_root"`
	VideosExportedRoot    string            `mapstructure:"videos_exported_root"`
	LocalVideos           LocalVideosConfig `mapstructure:"-"`

	GooglePhotos GooglePhotosConfig `mapstructure:"google_photos"`

	path string `mapstructure:"-"`
}

type LocalPhotosConfig struct {
	ToProcessRoot  string `mapstructure:"photos_to_process_root"`
	ExportQueueDir string `mapstructure:"photos_export_queue_dir"`
	ExportedRoot   string `mapstructure:"photos_exported_root"`
}

func (c *LocalPhotosConfig) GetExportQueueRoot() string {
	return c.ExportQueueDir
}

func (c *LocalPhotosConfig) ExportQueueIsFlat() bool {
	return true
}

func (c *LocalPhotosConfig) GetExportedRoot() string {
	return c.ExportedRoot
}

func (c *LocalVideosConfig) ExportQueueIsFlat() bool {
	return false
}

type LocalVideosConfig struct {
	ExportQueueRoot string `mapstructure:"videos_export_queue_root"`
	ExportedRoot    string `mapstructure:"videos_exported_root"`
}

func (c *LocalVideosConfig) GetExportQueueRoot() string {
	return c.ExportQueueRoot
}

func (c *LocalVideosConfig) GetExportedRoot() string {
	return c.ExportedRoot
}

func (c *GooglePhotosConfig) Validate() error {
	// Check that at least a base set of fields have values.
	if c.ClientId == "" || c.ClientSecret == "" {
		return fmt.Errorf("missing google photos client_id or client_secret")
	}
	if c.RedirectURI == "" {
		c.RedirectURI = "http://localhost:8080" // Default redirect URI
		fmt.Printf("Warning: google_photos.redirect_uri not set in config, using default: %s\n", c.RedirectURI)
	}
	// Allow empty DefaultAlbums, ToFavAlbumName, and KeywordAlbums.
	return nil
}

func (c *CamflowConfig) Validate() error {
	// Check that at least a base set of fields have values.
	if c.PhotosToProcessRoot == "" || c.PhotosExportQueueDir == "" || c.PhotosExportedRoot == "" {
		return fmt.Errorf("missing photos field (%s)", c.path)
	}
	if c.VideosExportQueueRoot == "" || c.VideosExportedRoot == "" {
		return fmt.Errorf("missing videos field (%s)", c.path)
	}
	if c.PhotosToProcessRoot != c.LocalPhotos.ToProcessRoot ||
		c.PhotosExportQueueDir != c.LocalPhotos.ExportQueueDir ||
		c.PhotosExportedRoot != c.LocalPhotos.ExportedRoot {
		return fmt.Errorf("local_photos config does not match flat fields (%s)", c.path)
	}
	if c.VideosExportQueueRoot != c.LocalVideos.ExportQueueRoot ||
		c.VideosExportedRoot != c.LocalVideos.ExportedRoot {
		return fmt.Errorf("local_videos config does not match flat fields (%s)", c.path)
	}
	if err := c.GooglePhotos.Validate(); err != nil {
		return fmt.Errorf("invalid google_photos config (%s): %w", c.path, err)
	}
	return nil
}

// DefaultConfigPath returns the default path for the Camflow config file.
func DefaultConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("unable to determine user config dir: %w", err)
	}
	return filepath.Join(dir, "camflow", "config.toml"), nil
}

// getConfigPath determines where to store the config file.
func getConfigPath(configPathFlag string) (string, error) {
	// Prefer user-specific config file path if specified.
	if configPathFlag != "" {
		return configPathFlag, nil
	}

	// Fall back to user config dir.
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "camflow", "config.toml"), nil
	}
	return "", fmt.Errorf("unable to determine config file path")
}

// loadConfig reads the config file.
func LoadConfig(configPathFlag string) (CamflowConfig, error) {
	path, err := getConfigPath(configPathFlag)
	if err != nil {
		return CamflowConfig{}, err
	}
	viper.SetConfigFile(path)
	viper.SetConfigType("toml")

	if err := viper.ReadInConfig(); err != nil {
		return CamflowConfig{}, fmt.Errorf("error reading (%s): %w", path, err)
	}
	config := CamflowConfig{path: path}
	if err := viper.Unmarshal(&config); err != nil {
		return CamflowConfig{}, fmt.Errorf("error unmarshaling (%s): %w", path, err)
	}
	config.LocalPhotos = LocalPhotosConfig{
		ToProcessRoot:  config.PhotosToProcessRoot,
		ExportQueueDir: config.PhotosExportQueueDir,
		ExportedRoot:   config.PhotosExportedRoot,
	}
	config.LocalVideos = LocalVideosConfig{
		ExportQueueRoot: config.VideosExportQueueRoot,
		ExportedRoot:    config.VideosExportedRoot,
	}

	return config, nil
}
