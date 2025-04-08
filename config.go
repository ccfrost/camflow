package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

// CamediaConfig defines the configuration for Camedia.
type CamediaConfig struct {
	DefaultAlbums []string `mapstructure:"default_albums"`

	PhotoRoot             string `mapstructure:"photo_root"`
	ToFavAlbumMinNumStars int    `mapstructure:"to_fav_album_min_num_stars"`
	ToFavAlbumName        string `mapstructure:"to_fav_album_name"`
	// TODO: add custom keywords to albums

	// TODO: connect to gphotos
	// TODO: connect to todoist
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
		return filepath.Join(dir, camedia, defaultFilename), nil
	}

	// Fall back to home directory.
	if dir, err := os.UserHomeDir(); err == nil {
		return filepath.Join(dir, defaultFilename), nil
	}

	return "", fmt.Errorf("unable to determine config file path")
}

// loadConfig reads the config file.
func loadConfig(configPathFlag string) (*CamediaConfig, error) {
	path, err := getConfigPath(configPathFlag)
	if err != nil {
		return nil, err
	}
	viper.SetConfigFile(path)
	viper.SetConfigType("toml")

	if err := viper.ReadInConfig(); err != nil {
		return nil, err
	}
	var config CamediaConfig
	if err := viper.Unmarshal(&config); err != nil {
		return nil, err
	}
	return &config, nil
}

// saveConfig writes the config to a file.
// TODO: unused and not fully implemented
func saveConfig(configPathFlag string, config *CamediaConfig) error {
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
