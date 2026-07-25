package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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

func (c *GPPhotosConfig) GetLabelAlbums() []KeyAlbum {
	return c.LabelAlbums
}

func (c *GPPhotosConfig) GetSubjectAlbums() []KeyAlbum {
	return c.SubjectAlbums
}

// GPVideosConfig defines the configuration for Videos in Google Photos.
type GPVideosConfig struct {
	DefaultAlbum string `mapstructure:"default_album"`
}

func (c *GPVideosConfig) GetDefaultAlbum() string {
	return c.DefaultAlbum
}

func (c *GPVideosConfig) GetLabelAlbums() []KeyAlbum {
	return nil
}

func (c *GPVideosConfig) GetSubjectAlbums() []KeyAlbum {
	return nil
}

// TODO: rename to camflow.
// CamflowConfig defines the configuration for Camflow.
// TODO: move flat fields into the new structs.
type CamflowConfig struct {
	PhotosProcessQueueRoot string            `mapstructure:"photos_process_queue_root"`
	PhotosUploadQueueDir   string            `mapstructure:"photos_upload_queue_dir"`
	PhotosUploadedRoot     string            `mapstructure:"photos_uploaded_root"`
	LocalPhotos            LocalPhotosConfig `mapstructure:"-"`

	VideosUploadQueueRoot string            `mapstructure:"videos_upload_queue_root"`
	VideosUploadedRoot    string            `mapstructure:"videos_uploaded_root"`
	LocalVideos           LocalVideosConfig `mapstructure:"-"`

	GooglePhotos GooglePhotosConfig `mapstructure:"google_photos"`

	path string `mapstructure:"-"`
}

type LocalPhotosConfig struct {
	ProcessQueueRoot string `mapstructure:"photos_process_queue_root"`
	UploadQueueDir   string `mapstructure:"photos_upload_queue_dir"`
	UploadedRoot     string `mapstructure:"photos_uploaded_root"`
}

func (c *LocalPhotosConfig) GetUploadQueueRoot() string {
	return c.UploadQueueDir
}

func (c *LocalPhotosConfig) GetUploadedRoot() string {
	return c.UploadedRoot
}

type LocalVideosConfig struct {
	UploadQueueRoot string `mapstructure:"videos_upload_queue_root"`
	UploadedRoot    string `mapstructure:"videos_uploaded_root"`
}

func (c *LocalVideosConfig) GetUploadQueueRoot() string {
	return c.UploadQueueRoot
}

func (c *LocalVideosConfig) GetUploadedRoot() string {
	return c.UploadedRoot
}

// defaultRedirectURI is used when redirect_uri is unset or a legacy OOB value that Google
// no longer supports for the loopback flow.
const defaultRedirectURI = "http://127.0.0.1:8080"

func (c *GooglePhotosConfig) Validate() error {
	// Check that at least a base set of fields have values.
	if c.ClientId == "" || c.ClientSecret == "" {
		return fmt.Errorf("missing google photos client_id or client_secret")
	}
	switch c.RedirectURI {
	case "":
		c.RedirectURI = defaultRedirectURI
		fmt.Fprintf(os.Stderr, "Warning: google_photos.redirect_uri not set in config, using default: %s\n", c.RedirectURI)
	case "urn:ietf:wg:oauth:2.0:oob":
		fmt.Fprintf(os.Stderr, "Warning: google_photos.redirect_uri is legacy OOB (%s). Overriding with %s for the loopback auth flow.\n", c.RedirectURI, defaultRedirectURI)
		c.RedirectURI = defaultRedirectURI
	}
	// Validate syntax now so a malformed redirect_uri fails at config load rather than at
	// the start of an upload.
	if _, err := ParseOAuthLoopbackRedirectURI(c.RedirectURI); err != nil {
		return err
	}
	// Allow empty DefaultAlbums, ToFavAlbumName, and KeywordAlbums.
	return nil
}

// ParseOAuthLoopbackRedirectURI validates that raw is a usable OAuth loopback redirect URI
// (http scheme, localhost/127.0.0.1/::1, and an explicit port) and returns the parsed URL so
// callers can bind a listener to its host. The loopback restriction follows Google's
// requirements for the installed-app OAuth flow.
func ParseOAuthLoopbackRedirectURI(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid google_photos.redirect_uri %q: %w", raw, err)
	}
	if !strings.EqualFold(u.Scheme, "http") {
		return nil, fmt.Errorf("invalid google_photos.redirect_uri %q: must use the http scheme", raw)
	}
	if u.User != nil {
		return nil, fmt.Errorf("invalid google_photos.redirect_uri %q: must not contain user information", raw)
	}
	if u.RawQuery != "" || u.ForceQuery {
		return nil, fmt.Errorf("invalid google_photos.redirect_uri %q: must not contain a query string", raw)
	}
	if u.Fragment != "" {
		return nil, fmt.Errorf("invalid google_photos.redirect_uri %q: must not contain a fragment", raw)
	}

	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("invalid google_photos.redirect_uri %q: must include a loopback host and explicit port (for example, http://127.0.0.1:8080)", raw)
	}
	if !strings.EqualFold(host, "localhost") && host != "127.0.0.1" && host != "::1" {
		return nil, fmt.Errorf("invalid google_photos.redirect_uri %q: loopback host must be localhost, 127.0.0.1, or ::1", raw)
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return nil, fmt.Errorf("invalid google_photos.redirect_uri %q: port must be a number from 1 through 65535", raw)
	}
	return u, nil
}

func (c *CamflowConfig) Validate() error {
	// Check that at least a base set of fields have values.
	if c.PhotosProcessQueueRoot == "" || c.PhotosUploadQueueDir == "" || c.PhotosUploadedRoot == "" {
		return fmt.Errorf("missing photos field (%s)", c.path)
	}
	if c.VideosUploadQueueRoot == "" || c.VideosUploadedRoot == "" {
		return fmt.Errorf("missing videos field (%s)", c.path)
	}
	if c.PhotosProcessQueueRoot != c.LocalPhotos.ProcessQueueRoot ||
		c.PhotosUploadQueueDir != c.LocalPhotos.UploadQueueDir ||
		c.PhotosUploadedRoot != c.LocalPhotos.UploadedRoot {
		return fmt.Errorf("local_photos config does not match flat fields (%s)", c.path)
	}
	if c.VideosUploadQueueRoot != c.LocalVideos.UploadQueueRoot ||
		c.VideosUploadedRoot != c.LocalVideos.UploadedRoot {
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

	// Allow users to override config values with environment variables.
	// In particular, may be desired for the Google Photos API credentials.
	viper.SetEnvPrefix("CAMFLOW")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		return CamflowConfig{}, fmt.Errorf("error reading (%s): %w", path, err)
	}
	config := CamflowConfig{path: path}
	if err := viper.Unmarshal(&config); err != nil {
		return CamflowConfig{}, fmt.Errorf("error unmarshaling (%s): %w", path, err)
	}
	config.LocalPhotos = LocalPhotosConfig{
		ProcessQueueRoot: config.PhotosProcessQueueRoot,
		UploadQueueDir:   config.PhotosUploadQueueDir,
		UploadedRoot:     config.PhotosUploadedRoot,
	}
	config.LocalVideos = LocalVideosConfig{
		UploadQueueRoot: config.VideosUploadQueueRoot,
		UploadedRoot:    config.VideosUploadedRoot,
	}

	return config, nil
}
