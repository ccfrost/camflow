package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/ccfrost/camedia/camediaconfig"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// --- OAuth2 & Client Setup ---

const (
	googlePhotosScope = "https://www.googleapis.com/auth/photoslibrary.appendonly"
	tokenFileName     = "google_photos_token.json"
)

// GetAuthenticatedGooglePhotosClient creates an authenticated HTTP client using OAuth2 credentials.
// It handles token loading, refreshing, and saving.
// Takes configDir to locate the token file.
func GetAuthenticatedGooglePhotosClient(ctx context.Context, config camediaconfig.CamediaConfig, configDir string) (*http.Client, error) {
	if config.GooglePhotos.ClientId == "" || config.GooglePhotos.ClientSecret == "" {
		return nil, fmt.Errorf("google Photos ClientId or ClientSecret not configured")
	}

	// Use http://localhost:0 for auto-selected port if RedirectURI is empty,
	// otherwise use the configured one.
	redirectURI := config.GooglePhotos.RedirectURI
	if redirectURI == "" {
		// Using a fixed common port for simplicity as dynamic port requires a listener.
		redirectURI = "http://localhost:8080"
		fmt.Printf("Warning: google_photos.redirect_uri not set in config, using default: %s\n", redirectURI)
	}

	conf := &oauth2.Config{
		ClientID:     config.GooglePhotos.ClientId,
		ClientSecret: config.GooglePhotos.ClientSecret,
		RedirectURL:  redirectURI,
		Scopes:       []string{googlePhotosScope},
		Endpoint:     google.Endpoint,
	}

	tokenFilePath, err := getTokenFilePath(configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get token file path: %w", err)
	}

	token := &oauth2.Token{}
	tokenFile, err := os.Open(tokenFilePath)
	if err == nil {
		err = json.NewDecoder(tokenFile).Decode(token)
		tokenFile.Close()
		if err != nil {
			fmt.Printf("Error reading token file (%s), requesting new token: %v\n", tokenFilePath, err)
			token = nil // Force getting a new token
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to open token file %s: %w", tokenFilePath, err)
	} else {
		// File does not exist, need to get token
		token = nil
	}

	if token == nil || !token.Valid() {
		fmt.Println("OAuth token is invalid or missing, starting auth flow...")
		newToken, err := getTokenFromWeb(ctx, conf)
		if err != nil {
			return nil, err
		}
		token = newToken
		if err := saveToken(tokenFilePath, token); err != nil {
			// Log error but continue, maybe token is still usable in memory
			fmt.Printf("Warning: Failed to save token to %s: %v\n", tokenFilePath, err)
		}
		fmt.Printf("Token obtained and saved successfully to %s\n", tokenFilePath)
	}

	// The gphotosuploader library expects an http.Client, which oauth2.Config provides.
	return conf.Client(ctx, token), nil
}

// getTokenFilePath constructs the path to the token file based on the config directory.
func getTokenFilePath(configDir string) (string, error) {
	if configDir == "." || configDir == "" {
		return "", fmt.Errorf("config directory path is empty or invalid")
	}
	return filepath.Join(configDir, tokenFileName), nil
}

// saveToken saves the OAuth2 token to the specified file path.
func saveToken(path string, token *oauth2.Token) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("unable to cache oauth token: %w", err)
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(token)
}

// getTokenFromWeb guides the user through the web-based OAuth2 flow.
func getTokenFromWeb(ctx context.Context, conf *oauth2.Config) (*oauth2.Token, error) {
	authURL := conf.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser then type the "+
		"authorization code: \n%v\n", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		return nil, fmt.Errorf("unable to read authorization code: %w", err)
	}

	tok, err := conf.Exchange(ctx, authCode)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve token from web: %w", err)
	}
	return tok, nil
}
