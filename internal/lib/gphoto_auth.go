package lib

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/ccfrost/camflow/internal/config"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// --- OAuth2 & Client Setup ---

// GetAuthenticatedGooglePhotosClient creates an authenticated HTTP client using OAuth2 credentials.
// It handles token loading, refreshing, and saving.
// Takes cacheDir to locate the token file.
func GetAuthenticatedGooglePhotosClient(ctx context.Context, cfg config.CamflowConfig, cacheDir string) (*http.Client, error) {
	// Validate a local copy so direct library callers get the same redirect defaults and
	// legacy-OOB migration as the CLI without mutating their configuration.
	googlePhotosCfg := cfg.GooglePhotos
	if err := googlePhotosCfg.Validate(); err != nil {
		return nil, err
	}

	conf := &oauth2.Config{
		ClientID:     googlePhotosCfg.ClientId,
		ClientSecret: googlePhotosCfg.ClientSecret,
		RedirectURL:  googlePhotosCfg.RedirectURI,
		Scopes: []string{
			"https://www.googleapis.com/auth/photoslibrary.readonly.appcreateddata",
			"https://www.googleapis.com/auth/photoslibrary.appendonly",
			"https://www.googleapis.com/auth/photoslibrary.edit.appcreateddata",
		},
		Endpoint: google.Endpoint,
	}

	tokenFilePath := getTokenFilePath(cacheDir)
	if err := ensureCacheDir(cacheDir); err != nil {
		return nil, err
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

	src, err := authenticatedTokenSource(ctx, conf, token, tokenFilePath, runInteractiveAuth)
	if err != nil {
		return nil, err
	}

	// The gphotosuploader library expects an http.Client. Keep src directly on the
	// transport: oauth2.NewClient would wrap it in another ReuseTokenSource, which
	// would prevent persistingTokenSource from retrying a failed save until the
	// cached access token expires. The source wrapped by persistingTokenSource
	// already handles normal token reuse and refresh.
	return newOAuthClient(ctx, src), nil
}

func newOAuthClient(ctx context.Context, src oauth2.TokenSource) *http.Client {
	base := oauth2.NewClient(ctx, nil)
	return &http.Client{
		Transport: &oauth2.Transport{
			Base:   base.Transport,
			Source: src,
		},
		CheckRedirect: base.CheckRedirect,
		Jar:           base.Jar,
		Timeout:       base.Timeout,
	}
}

const defaultOAuthRequestTimeout = 60 * time.Second

// withOAuthRequestTimeout returns a context whose HTTP client is used only by
// golang.org/x/oauth2 token requests. It clones the caller's client so adding the
// default timeout never mutates shared state, and it does not add a context deadline:
// browser consent can take as long as needed while each token HTTP request is bounded.
func withOAuthRequestTimeout(ctx context.Context) context.Context {
	client := http.DefaultClient
	if configured, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok && configured != nil {
		client = configured
	}
	bounded := *client
	if bounded.Timeout <= 0 {
		bounded.Timeout = defaultOAuthRequestTimeout
	}
	return context.WithValue(ctx, oauth2.HTTPClient, &bounded)
}

type interactiveAuthFunc func(context.Context, *oauth2.Config) (*oauth2.Token, error)

// authenticatedTokenSource selects interactive authentication or validates stored
// credentials with a real refresh-token exchange. Supplying only the refresh token to
// conf.TokenSource guarantees the preflight contacts the token endpoint even when the
// stored access token has not reached its recorded expiry yet.
func authenticatedTokenSource(ctx context.Context, conf *oauth2.Config, token *oauth2.Token, tokenFilePath string, interactiveAuth interactiveAuthFunc) (oauth2.TokenSource, error) {
	ctx = withOAuthRequestTimeout(ctx)

	if token == nil {
		fmt.Println("No existing OAuth token found, starting auth flow...")
	} else if token.RefreshToken == "" {
		// Without a refresh token the access token dies within the hour and cannot be
		// renewed silently (eg, a token file saved before offline access was requested);
		// re-auth now instead of failing mid-upload.
		fmt.Println("OAuth token has no refresh token, starting auth flow...")
		token = nil
	}

	if token == nil {
		return newInteractiveTokenSource(ctx, conf, tokenFilePath, interactiveAuth)
	}

	// Deliberately omit the cached access token so Token must exchange the stored refresh
	// token. This catches revoked credentials before any media preparation or upload begins.
	refreshSeed := &oauth2.Token{RefreshToken: token.RefreshToken}
	src := &persistingTokenSource{
		src:  conf.TokenSource(ctx, refreshSeed),
		path: tokenFilePath,
		last: token,
	}
	if _, err := src.Token(); err != nil {
		if !storedCredentialRejected(err) {
			return nil, fmt.Errorf("could not refresh Google credentials: %w (if this persists, delete %s to force re-authentication)", err, tokenFilePath)
		}
		fmt.Printf("Stored Google credentials were rejected (%v), starting auth flow...\n", err)
		return newInteractiveTokenSource(ctx, conf, tokenFilePath, interactiveAuth)
	}
	return src, nil
}

func newInteractiveTokenSource(ctx context.Context, conf *oauth2.Config, tokenFilePath string, interactiveAuth interactiveAuthFunc) (oauth2.TokenSource, error) {
	token, err := interactiveAuth(ctx, conf)
	if err != nil {
		return nil, err
	}
	if err := validateInteractiveToken(token); err != nil {
		return nil, err
	}

	// Keep an unsaved token usable in memory, but leave last nil so every subsequent
	// Token call retries persistence until the cache becomes writable.
	var last *oauth2.Token
	if err := saveToken(tokenFilePath, token); err != nil {
		fmt.Printf("Warning: Failed to save token to %s: %v\n", tokenFilePath, err)
	} else {
		fmt.Printf("Token obtained and saved successfully to %s\n", tokenFilePath)
		last = token
	}

	return &persistingTokenSource{
		src:  conf.TokenSource(ctx, token),
		path: tokenFilePath,
		last: last,
	}, nil
}

// runInteractiveAuth obtains a token through the browser OAuth flow. The caller validates
// and persists the token so injected interactive flows follow the same behavior.
func runInteractiveAuth(ctx context.Context, conf *oauth2.Config) (*oauth2.Token, error) {
	return getTokenFromWeb(ctx, conf)
}

func validateInteractiveToken(token *oauth2.Token) error {
	if token == nil || !token.Valid() {
		return fmt.Errorf("Google authentication returned no usable access token")
	}
	if token.RefreshToken == "" {
		return fmt.Errorf("Google authentication returned no refresh token; verify the OAuth client configuration and try again")
	}
	return nil
}

// storedCredentialRejected reports whether err means the OAuth server rejected the
// stored refresh token itself (revoked, expired, etc — RFC 6749 invalid_grant), as
// opposed to a transient network/server/rate-limit failure that re-auth wouldn't fix.
func storedCredentialRejected(err error) bool {
	var rErr *oauth2.RetrieveError
	return errors.As(err, &rErr) && rErr.ErrorCode == "invalid_grant"
}

func oauthTokenRequestError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("OAuth token request timed out; check the network connection and try again: %w", err)
	}
	return err
}

// persistingTokenSource wraps a TokenSource and writes the token to disk whenever the
// wrapped source hands back a different one (ie, after a silent refresh).
type persistingTokenSource struct {
	src  oauth2.TokenSource
	path string
	mu   sync.Mutex
	last *oauth2.Token
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	// The lock spans the fetch so concurrent refreshes can't persist out of order; the
	// wrapped ReuseTokenSource serializes refreshes anyway, so this adds no contention.
	p.mu.Lock()
	defer p.mu.Unlock()
	tok, err := p.src.Token()
	if err != nil {
		return nil, oauthTokenRequestError(err)
	}
	if p.last == nil || tok.AccessToken != p.last.AccessToken || tok.RefreshToken != p.last.RefreshToken || !tok.Expiry.Equal(p.last.Expiry) {
		if err := saveToken(p.path, tok); err != nil {
			fmt.Printf("Warning: failed to persist refreshed Google token to %s: %v\n", p.path, err)
		} else {
			p.last = tok
		}
	}
	return tok, nil
}

// getTokenFilePath determines where to store the token file.
func getTokenFilePath(cacheDir string) string {
	return filepath.Join(cacheDir, "google_photos_token.json")
}

func ensureCacheDir(cacheDir string) error {
	if cacheDir == "" {
		return fmt.Errorf("cache directory is empty")
	}
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return fmt.Errorf("failed to create cache directory %s: %w", cacheDir, err)
	}
	info, err := os.Stat(cacheDir)
	if err != nil {
		return fmt.Errorf("failed to inspect cache directory %s: %w", cacheDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("cache path is not a directory: %s", cacheDir)
	}
	return nil
}

// staleTokenTempAge bounds how long a legitimate token temp file can plausibly live
// (encode + fsync + rename take milliseconds). Anything older is an orphan from a crashed
// run and is safe to remove, so a concurrent writer's in-flight temp is never touched. Set
// generously (an hour) since the only cost of waiting is an orphan lingering slightly
// longer before the next save reclaims it.
const staleTokenTempAge = time.Hour

// removeStaleTokenTempFiles deletes leftover "<base>.tmp-*" files from token writes that
// were killed between CreateTemp and Rename. Best-effort: any error is ignored so cleanup
// never blocks saving the token.
func removeStaleTokenTempFiles(path string) {
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), filepath.Base(path)+".tmp-*"))
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-staleTokenTempAge)
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(m)
		}
	}
}

// saveToken saves the OAuth2 token to the specified file path. It writes to a temp file
// and renames so a crash mid-write can't leave a corrupt token file behind.
func saveToken(path string, token *oauth2.Token) error {
	if err := ensureCacheDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("unable to cache oauth token: %w", err)
	}
	// Reclaim temp files orphaned by a previous run that was killed between CreateTemp and
	// Rename. Done before we create our own temp so it is never a removal candidate.
	removeStaleTokenTempFiles(path)
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("unable to cache oauth token: %w", err)
	}
	if err := json.NewEncoder(f).Encode(token); err != nil {
		f.Close()
		os.Remove(f.Name())
		return fmt.Errorf("unable to cache oauth token: %w", err)
	}
	// Flush to stable storage before the rename so a power loss can't make the rename
	// durable while the data isn't (which would leave an empty/corrupt token file).
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(f.Name())
		return fmt.Errorf("unable to cache oauth token: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return fmt.Errorf("unable to cache oauth token: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		os.Remove(f.Name())
		return fmt.Errorf("unable to cache oauth token: %w", err)
	}
	return nil
}

type authCallbackResult struct {
	code string
	err  error
}

// getTokenFromWeb guides the user through the web-based OAuth2 flow via a local server.
func getTokenFromWeb(ctx context.Context, conf *oauth2.Config) (*oauth2.Token, error) {
	return getTokenFromWebWithBrowser(ctx, conf, openBrowser)
}

// getTokenFromWebWithBrowser contains the OAuth flow with an injectable browser opener
// so the complete loopback flow can be exercised without launching a real browser.
func getTokenFromWebWithBrowser(ctx context.Context, conf *oauth2.Config, browserOpener func(string)) (*oauth2.Token, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	u, err := config.ParseOAuthLoopbackRedirectURI(conf.RedirectURL)
	if err != nil {
		return nil, err
	}

	resultCh := make(chan authCallbackResult, 1)

	l, err := net.Listen("tcp", u.Host)
	if err != nil {
		return nil, fmt.Errorf("failed to start local server for auth on %s: %w (if the port is in use, free it or set google_photos.redirect_uri to a different http://127.0.0.1:<port>)", u.Host, err)
	}
	defer l.Close()
	// fmt.Printf("Listening on %s for authentication callback...\n", l.Addr().String())

	// A random state ties the callback to this auth attempt: the handler rejects codes
	// delivered by requests that didn't originate from our auth URL (OAuth CSRF).
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return nil, fmt.Errorf("failed to generate OAuth state: %w", err)
	}
	state := hex.EncodeToString(stateBytes)

	// PKCE (RFC 7636): the token exchange must present the verifier matching the
	// code_challenge sent in the auth URL, so a stolen authorization code is useless on
	// its own. State stops an attacker injecting their code; PKCE stops one using ours.
	verifier := oauth2.GenerateVerifier()

	server := &http.Server{Handler: authCallbackHandler(state, resultCh), ReadHeaderTimeout: 10 * time.Second}

	go func() {
		if err := server.Serve(l); err != nil && err != http.ErrServerClosed {
			select {
			case resultCh <- authCallbackResult{err: fmt.Errorf("http server error: %w", err)}:
			default: // the flow already has a result; nothing reads resultCh anymore
			}
		}
	}()
	// Tear the server down gracefully on every exit path. Shutdown waits for the callback
	// handler to finish flushing its browser response; the bounded fallback still prevents
	// a slow or malicious connection from keeping the CLI alive indefinitely.
	shutdownServer := sync.OnceFunc(func() { shutdownOAuthCallbackServer(server) })
	defer shutdownServer()

	// ApprovalForce (prompt=consent) makes Google issue a refresh token on every
	// interactive auth, not just the first consent; without it a re-auth saves a token
	// file with no refresh token and hourly browser prompts return.
	authURL := conf.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce, oauth2.S256ChallengeOption(verifier))
	fmt.Printf("Opening browser to complete authentication:\n%s\n", authURL)

	// Avoid opening a browser when cancellation happened while the listener and auth URL
	// were being prepared. The deferred shutdown still closes the listener in this case.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	go browserOpener(authURL)

	fmt.Println("Waiting for authentication callback...")

	select {
	case result := <-resultCh:
		// Stop accepting callbacks as soon as the first result wins. Shutdown waits for the
		// active handler to flush its browser response before the token exchange starts.
		shutdownServer()
		if result.err != nil {
			return nil, result.err
		}
		tok, err := conf.Exchange(ctx, result.code, oauth2.VerifierOption(verifier))
		if err != nil {
			return nil, fmt.Errorf("unable to retrieve token from web exchange: %w", oauthTokenRequestError(err))
		}
		return tok, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func shutdownOAuthCallbackServer(server *http.Server) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
	}
}

// authCallbackHandler handles the OAuth redirect back to the local server. A result is
// delivered only when the state parameter matches this auth attempt, so a code injected
// by a request that didn't come from our auth URL (OAuth CSRF) is ignored. A denial (error
// param, eg the user canceling the consent screen) fails the flow immediately instead of
// waiting for a code that will never arrive.
func authCallbackHandler(state string, resultCh chan<- authCallbackResult) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		errParam := q.Get("error")
		code := q.Get("code")
		if errParam == "" && code == "" {
			if r.URL.Path != "/favicon.ico" {
				fmt.Printf("Warning: Code not found in request (path: %s)\n", r.URL.Path)
			}
			http.Error(w, "Code not found in response", http.StatusBadRequest)
			return
		}
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			// Keep listening: a stray or forged request must not kill a pending auth.
			fmt.Println("Warning: Ignoring auth callback with mismatched state parameter")
			http.Error(w, "State mismatch", http.StatusBadRequest)
			return
		}

		if errParam != "" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><h1>Camflow Authentication Failed</h1><p>You can close this window and return to the terminal.</p></body></html>`)
			if desc := q.Get("error_description"); desc != "" {
				errParam += ": " + desc
			}
			select {
			case resultCh <- authCallbackResult{err: fmt.Errorf("authentication failed: %s", errParam)}:
			default: // an earlier callback already delivered a result; don't block the handler
			}
			return
		}

		// The authorization code has arrived, but the token exchange still happens after
		// this response is flushed. Do not claim authentication succeeded prematurely.
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><h1>Camflow Authorization Received</h1><p>You can close this window and return to the terminal. Camflow will report whether authentication completed successfully.</p></body></html>`)

		select {
		case resultCh <- authCallbackResult{code: code}:
		default: // duplicate success callback; the first one already delivered
		}
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
