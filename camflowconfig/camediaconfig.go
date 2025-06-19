package camflowconfig

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

type KeywordAlbum struct {
	Keyword string `mapstructure:"keyword"`
	Album   string `mapstructure:"album"`
}

// GooglePhotosConfig defines the configuration specific to Google Photos.
type GooglePhotosConfig struct {
	ClientId     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`
	RedirectURI  string `mapstructure:"redirect_uri"`

	DefaultAlbums []string `mapstructure:"default_albums"`

	ToFavAlbumMinNumStars int            `mapstructure:"to_fav_album_min_num_stars"`
	ToFavAlbumName        string         `mapstructure:"to_fav_album_name"`
	KeywordAlbums         []KeywordAlbum `mapstructure:"keyword_albums"`
}

// CamediaConfig defines the configuration for Camedia.
type CamediaConfig struct {
	PhotosOrigRoot         string `mapstructure:"photos_orig_root"`
	PhotosExportStagingDir string `mapstructure:"photos_export_staging_dir"`
	PhotosExportDir        string `mapstructure:"photos_export_dir"`

	VideosOrigStagingRoot string `mapstructure:"videos_orig_staging_root"`
	VideosOrigRoot        string `mapstructure:"videos_orig_root"`

	GooglePhotos GooglePhotosConfig `mapstructure:"google_photos"`

	// TODO: connect to todoist
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

func (c *CamediaConfig) Validate() error {
	// Check that at least a base set of fields have values.
	if c.PhotosOrigRoot == "" || c.PhotosExportStagingDir == "" || c.PhotosExportDir == "" {
		return fmt.Errorf("missing photos field")
	}
	if c.VideosOrigStagingRoot == "" || c.VideosOrigRoot == "" {
		return fmt.Errorf("missing videos field")
	}
	return c.GooglePhotos.Validate()
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
func LoadConfig(configPathFlag string) (CamediaConfig, error) {
	path, err := getConfigPath(configPathFlag)
	if err != nil {
		return CamediaConfig{}, err
	}
	viper.SetConfigFile(path)
	viper.SetConfigType("toml")

	if err := viper.ReadInConfig(); err != nil {
		return CamediaConfig{}, err
	}
	var config CamediaConfig
	if err := viper.Unmarshal(&config); err != nil {
		return CamediaConfig{}, err
	}

	return config, nil
}
