package camediaconfig

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
	// The path from which this config was loaded.
	configPath string `mapstructure:"-"` // Mark as transient for mapstructure

	PhotosOrigRoot         string `mapstructure:"photos_orig_root"`
	PhotosExportStagingDir string `mapstructure:"photos_export_staging_dir"`
	PhotosExportDir        string `mapstructure:"photos_export_dir"`

	VideosOrigStagingRoot string `mapstructure:"videos_orig_staging_root"`
	VideosOrigRoot        string `mapstructure:"videos_orig_root"`

	GooglePhotos GooglePhotosConfig `mapstructure:"google_photos"`

	// TODO: connect to todoist
}

// ConfigPath returns the absolute path from which the configuration was loaded.
func (c *CamediaConfig) ConfigPath() string {
	return c.configPath
}

func (c *CamediaConfig) Validate() error {
	// Check that at least a base set of fields have values.
	if c.PhotosOrigRoot == "" || c.PhotosExportStagingDir == "" || c.PhotosExportDir == "" {
		return fmt.Errorf("missing photos field")
	}
	if c.VideosOrigStagingRoot == "" || c.VideosOrigRoot == "" {
		return fmt.Errorf("missing videos field")
	}
	// TODO: validate any other fields?
	return nil
}

// getConfigPath determines where to store the config file.
func getConfigPath(configPathFlag string) (string, error) {
	// Prefer user-specific config file path if specified.
	if configPathFlag != "" {
		return configPathFlag, nil
	}

	const defaultFilename = "config.toml"

	// Fall back to user config dir.
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "camedia", defaultFilename), nil
	}

	// Fall back to home directory.
	if dir, err := os.UserHomeDir(); err == nil {
		return filepath.Join(dir, defaultFilename), nil
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

	// Store the path from which the config was loaded.
	config.configPath = path

	return config, nil
}

// saveConfig writes the config to a file.
// TODO: unused and not fully implemented
func saveConfig(configPathFlag string, config CamediaConfig) error {
	path, err := getConfigPath(configPathFlag)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	/* TODO:
	   viper.Set("username", config.Username)
	   viper.Set("api_key", config.APIKey)
	*/

	return viper.WriteConfigAs(path)
}
