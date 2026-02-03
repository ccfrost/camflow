package lib

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/ccfrost/camflow/internal/config"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// --- OAuth2 & Client Setup ---

// GetAuthenticatedGooglePhotosClient creates an authenticated HTTP client using OAuth2 credentials.
// It handles token loading, refreshing, and saving.
// Takes configDir to locate the token file.
func GetAuthenticatedGooglePhotosClient(ctx context.Context, cfg config.CamflowConfig, cacheDir string) (*http.Client, error) {
	if cfg.GooglePhotos.ClientId == "" || cfg.GooglePhotos.ClientSecret == "" {
		return nil, fmt.Errorf("google Photos ClientId or ClientSecret not configured")
	}

	// Use http://localhost:0 for auto-selected port if RedirectURI is empty,
	// otherwise use the configured one.
	redirectURI := cfg.GooglePhotos.RedirectURI
	if redirectURI == "" || redirectURI == "urn:ietf:wg:oauth:2.0:oob" {
		// Using a fixed common port for simplicity as dynamic port requires a listener.
		redirectURI = "http://localhost:8080"
		if cfg.GooglePhotos.RedirectURI == "urn:ietf:wg:oauth:2.0:oob" {
			fmt.Printf("Warning: google_photos.redirect_uri is legacy OOB (%s). Overriding with %s for new auth flow.\n", cfg.GooglePhotos.RedirectURI, redirectURI)
		} else {
			fmt.Printf("Warning: google_photos.redirect_uri not set in config, using default: %s\n", redirectURI)
		}
	}

	conf := &oauth2.Config{
		ClientID:     cfg.GooglePhotos.ClientId,
		ClientSecret: cfg.GooglePhotos.ClientSecret,
		RedirectURL:  redirectURI,
		Scopes: []string{
			"https://www.googleapis.com/auth/photoslibrary.readonly.appcreateddata",
			"https://www.googleapis.com/auth/photoslibrary.appendonly",
			"https://www.googleapis.com/auth/photoslibrary.edit.appcreateddata",
		},
		Endpoint: google.Endpoint,
	}

	tokenFilePath := getTokenFilePath(cacheDir)

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
		if token == nil {
			fmt.Println("No existing OAuth token found, starting auth flow...")
		} else {
			fmt.Println("OAuth token is invalid (eg, expired), starting auth flow...")
		}
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

// getTokenFilePath determines where to store the token file.
func getTokenFilePath(cacheDir string) string {
	return filepath.Join(cacheDir, "google_photos_token.json")
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

// getTokenFromWeb guides the user through the web-based OAuth2 flow via a local server.
func getTokenFromWeb(ctx context.Context, conf *oauth2.Config) (*oauth2.Token, error) {
	// Parse the redirect URL to determine the port to listen on.
	// We expect something like "http://localhost:8080" or "http://127.0.0.1:8080"
	u, err := url.Parse(conf.RedirectURL)
	if err != nil {
		return nil, fmt.Errorf("bad redirect URL: %w", err)
	}

	codeCh := make(chan string, 1) // Buffered channel
	errCh := make(chan error, 1)

	l, err := net.Listen("tcp", u.Host)
	if err != nil {
		return nil, fmt.Errorf("failed to start local server for auth: %w", err)
	}
	defer l.Close()
	// fmt.Printf("Listening on %s for authentication callback...\n", l.Addr().String())

	// Handler for the redirect.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			if r.URL.Path != "/favicon.ico" {
				fmt.Printf("Warning: Code not found in request (path: %s)\n", r.URL.Path)
			}
			http.Error(w, "Code not found in response", http.StatusBadRequest)
			return
		}

		// Show success message to user.
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><h1>Camflow Authentication Successful</h1><p>You can close this window now and return to the terminal.</p></body></html>`)

		codeCh <- code
	})

	server := &http.Server{Handler: handler}

	go func() {
		if err := server.Serve(l); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("http server error: %w", err)
		}
	}()

	authURL := conf.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Opening browser to complete authentication:\n%s\n", authURL)

	go openBrowser(authURL)

	fmt.Println("Waiting for authentication callback...")

	select {
	case code := <-codeCh:
		go server.Shutdown(context.Background())

		tok, err := conf.Exchange(ctx, code)
		if err != nil {
			return nil, fmt.Errorf("unable to retrieve token from web exchange: %w", err)
		}
		return tok, nil

	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// openBrowser attempts to open the specified URL in the default browser.
func openBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}
	if err != nil {
		fmt.Printf("Could not open browser automatically: %v\nPlease open the URL manually.\n", err)
	}
}
