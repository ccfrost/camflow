package lib

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ccfrost/camflow/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestStoredCredentialRejected(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"invalid_grant", &oauth2.RetrieveError{ErrorCode: "invalid_grant"}, true},
		{"wrapped invalid_grant", fmt.Errorf("probe: %w", &oauth2.RetrieveError{ErrorCode: "invalid_grant"}), true},
		{"invalid_client", &oauth2.RetrieveError{ErrorCode: "invalid_client"}, false},
		{"retrieve error without code (eg, bare 500)", &oauth2.RetrieveError{}, false},
		{"network error", errors.New("dial tcp: connection refused"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, storedCredentialRejected(tt.err))
		})
	}
}

type fakeTokenSource struct {
	tok *oauth2.Token
	err error
}

func (f *fakeTokenSource) Token() (*oauth2.Token, error) {
	return f.tok, f.err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestWithOAuthRequestTimeout(t *testing.T) {
	t.Run("adds default without adding context deadline", func(t *testing.T) {
		ctx := withOAuthRequestTimeout(context.Background())
		client, ok := ctx.Value(oauth2.HTTPClient).(*http.Client)
		require.True(t, ok)
		assert.NotSame(t, http.DefaultClient, client)
		assert.Equal(t, defaultOAuthRequestTimeout, client.Timeout)
		assert.Zero(t, http.DefaultClient.Timeout, "the shared default client must not be mutated")
		_, hasDeadline := ctx.Deadline()
		assert.False(t, hasDeadline, "the timeout must apply to token HTTP requests, not browser waiting")
	})

	t.Run("clones custom client and applies default", func(t *testing.T) {
		transportFunc := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
		transport := &transportFunc
		configured := &http.Client{Transport: transport}
		ctx := context.WithValue(context.Background(), oauth2.HTTPClient, configured)

		boundedCtx := withOAuthRequestTimeout(ctx)
		bounded := boundedCtx.Value(oauth2.HTTPClient).(*http.Client)
		assert.NotSame(t, configured, bounded)
		assert.Equal(t, defaultOAuthRequestTimeout, bounded.Timeout)
		assert.Same(t, transport, bounded.Transport)
		assert.Zero(t, configured.Timeout, "the caller's client must not be mutated")
	})

	t.Run("preserves explicit positive timeout", func(t *testing.T) {
		configured := &http.Client{Timeout: 2 * time.Minute}
		ctx := context.WithValue(context.Background(), oauth2.HTTPClient, configured)

		boundedCtx := withOAuthRequestTimeout(ctx)
		bounded := boundedCtx.Value(oauth2.HTTPClient).(*http.Client)
		assert.NotSame(t, configured, bounded)
		assert.Equal(t, configured.Timeout, bounded.Timeout)
	})
}

func TestPersistingTokenSourceWritesOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	oldTok := &oauth2.Token{AccessToken: "old", Expiry: time.Now().Add(-time.Hour)}
	newTok := &oauth2.Token{AccessToken: "new", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}

	src := &persistingTokenSource{src: &fakeTokenSource{tok: newTok}, path: path, last: oldTok}
	got, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, newTok, got)

	var saved oauth2.Token
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, "new", saved.AccessToken)
	assert.Equal(t, "refresh", saved.RefreshToken)
}

func TestPersistingTokenSourceNoWriteOnSameToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	tok := &oauth2.Token{AccessToken: "same", Expiry: time.Now().Add(time.Hour)}

	src := &persistingTokenSource{src: &fakeTokenSource{tok: tok}, path: path, last: tok}
	_, err := src.Token()
	require.NoError(t, err)

	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "token file should not be written when the token is unchanged")
}

func TestPersistingTokenSourcePropagatesErrorWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	src := &persistingTokenSource{src: &fakeTokenSource{err: errors.New("refresh failed")}, path: path}

	_, err := src.Token()
	require.Error(t, err)

	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}

func TestPersistingTokenSourceRetriesSaveAfterFailure(t *testing.T) {
	tokenDir := filepath.Join(t.TempDir(), "blocked")
	path := filepath.Join(tokenDir, "token.json")
	oldTok := &oauth2.Token{AccessToken: "old", Expiry: time.Now().Add(-time.Hour)}
	newTok := &oauth2.Token{AccessToken: "new", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}
	require.NoError(t, os.WriteFile(tokenDir, []byte("not a directory"), 0600))

	src := &persistingTokenSource{src: &fakeTokenSource{tok: newTok}, path: path, last: oldTok}
	got, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, newTok, got)

	_, err = os.Stat(path)
	assert.Error(t, err, "token file should not exist after the failed save")
	require.NoError(t, os.Remove(tokenDir))
	require.NoError(t, os.Mkdir(tokenDir, 0700))

	got, err = src.Token()
	require.NoError(t, err)
	assert.Equal(t, newTok, got)

	var saved oauth2.Token
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, newTok.AccessToken, saved.AccessToken)
	assert.Equal(t, newTok.RefreshToken, saved.RefreshToken)
}

func TestOAuthClientRetriesTokenPersistenceOnLaterRequests(t *testing.T) {
	tokenDir := filepath.Join(t.TempDir(), "blocked")
	path := filepath.Join(tokenDir, "token.json")
	token := &oauth2.Token{AccessToken: "access", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}
	require.NoError(t, os.WriteFile(tokenDir, []byte("not a directory"), 0600))

	src := &persistingTokenSource{src: &fakeTokenSource{tok: token}, path: path}
	requestCount := 0
	baseClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		assert.Equal(t, "Bearer access", req.Header.Get("Authorization"))
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, baseClient)
	client := newOAuthClient(ctx, src)

	// Keep persistence blocked across multiple requests. The client must continue
	// calling persistingTokenSource instead of caching the first usable token above it.
	for range 2 {
		response, err := client.Get("http://example.test/resource")
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
	}
	_, err := os.Stat(path)
	assert.Error(t, err, "token file should not exist while persistence remains blocked")

	require.NoError(t, os.Remove(tokenDir))
	require.NoError(t, os.Mkdir(tokenDir, 0700))
	response, err := client.Get("http://example.test/resource")
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, 3, requestCount)

	var saved oauth2.Token
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, token.AccessToken, saved.AccessToken)
	assert.Equal(t, token.RefreshToken, saved.RefreshToken)
}

func TestPersistingTokenSourceWritesOnRefreshTokenRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	expiry := time.Now().Add(time.Hour)
	oldTok := &oauth2.Token{AccessToken: "same", RefreshToken: "old-refresh", Expiry: expiry}
	newTok := &oauth2.Token{AccessToken: "same", RefreshToken: "new-refresh", Expiry: expiry}

	src := &persistingTokenSource{src: &fakeTokenSource{tok: newTok}, path: path, last: oldTok}
	_, err := src.Token()
	require.NoError(t, err)

	var saved oauth2.Token
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, "new-refresh", saved.RefreshToken)
}

func TestEnsureCacheDir(t *testing.T) {
	t.Run("creates private nested directory", func(t *testing.T) {
		cacheDir := filepath.Join(t.TempDir(), "nested", "cache")
		require.NoError(t, ensureCacheDir(cacheDir))

		info, err := os.Stat(cacheDir)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
		assert.Zero(t, info.Mode().Perm()&0077, "new cache directory must not grant group or world permissions")
	})

	t.Run("rejects a file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cache-file")
		require.NoError(t, os.WriteFile(path, []byte("not a directory"), 0600))

		err := ensureCacheDir(path)
		require.Error(t, err)
		assert.ErrorContains(t, err, "cache directory")
	})

	t.Run("rejects an empty path", func(t *testing.T) {
		assert.ErrorContains(t, ensureCacheDir(""), "empty")
	})
}

func TestSaveTokenCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "cache", "token.json")
	token := &oauth2.Token{AccessToken: "access", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}

	require.NoError(t, saveToken(path, token))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var saved oauth2.Token
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, token.AccessToken, saved.AccessToken)
	assert.Equal(t, token.RefreshToken, saved.RefreshToken)
}

func TestSaveTokenRemovesStaleTempFiles(t *testing.T) {
	token := &oauth2.Token{AccessToken: "access", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}

	t.Run("removes a stale orphan temp file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "google_photos_token.json")
		orphan := path + ".tmp-orphan"
		require.NoError(t, os.WriteFile(orphan, []byte("partial write"), 0600))
		old := time.Now().Add(-2 * time.Hour)
		require.NoError(t, os.Chtimes(orphan, old, old))

		require.NoError(t, saveToken(path, token))

		_, err := os.Stat(orphan)
		assert.True(t, os.IsNotExist(err), "an orphaned temp file from a crashed run should be swept")

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		var saved oauth2.Token
		require.NoError(t, json.Unmarshal(data, &saved))
		assert.Equal(t, token.AccessToken, saved.AccessToken)
	})

	t.Run("preserves a recent temp file from a concurrent writer", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "google_photos_token.json")
		fresh := path + ".tmp-recent"
		require.NoError(t, os.WriteFile(fresh, []byte("in flight"), 0600))

		require.NoError(t, saveToken(path, token))

		_, err := os.Stat(fresh)
		assert.NoError(t, err, "a recent temp file must not be swept while another process may be mid-write")
	})
}

func TestValidateInteractiveToken(t *testing.T) {
	valid := &oauth2.Token{AccessToken: "access", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}
	tests := []struct {
		name  string
		token *oauth2.Token
		match string
	}{
		{name: "valid", token: valid},
		{name: "nil", match: "usable access token"},
		{name: "missing access token", token: &oauth2.Token{RefreshToken: "refresh"}, match: "usable access token"},
		{name: "expired access token", token: &oauth2.Token{AccessToken: "access", RefreshToken: "refresh", Expiry: time.Now().Add(-time.Hour)}, match: "usable access token"},
		{name: "missing refresh token", token: &oauth2.Token{AccessToken: "access", Expiry: time.Now().Add(time.Hour)}, match: "no refresh token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateInteractiveToken(tt.token)
			if tt.match == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.match)
		})
	}
}

func TestAuthenticatedTokenSourceForcesStoredCredentialRefresh(t *testing.T) {
	var requests atomic.Int32
	refreshTokenCh := make(chan string, 1)
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		refreshTokenCh <- r.Form.Get("refresh_token")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"refreshed-access","token_type":"Bearer","expires_in":3600}`)
	}))
	defer tokenServer.Close()

	conf := testOAuthConfig(tokenServer.URL)
	path := filepath.Join(t.TempDir(), "new-cache", "token.json")
	stored := &oauth2.Token{
		AccessToken:  "still-valid-access",
		RefreshToken: "stored-refresh",
		Expiry:       time.Now().Add(time.Hour),
	}
	interactiveCalls := 0
	src, err := authenticatedTokenSource(context.Background(), conf, stored, path, func(context.Context, *oauth2.Config) (*oauth2.Token, error) {
		interactiveCalls++
		return nil, errors.New("interactive auth should not run")
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), requests.Load())
	assert.Equal(t, "stored-refresh", <-refreshTokenCh)
	assert.Zero(t, interactiveCalls)

	tok, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, "refreshed-access", tok.AccessToken)
	assert.Equal(t, "stored-refresh", tok.RefreshToken)
	assert.Equal(t, int32(1), requests.Load(), "the preflight token should be reused until it expires")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var saved oauth2.Token
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, tok.AccessToken, saved.AccessToken)
	assert.Equal(t, tok.RefreshToken, saved.RefreshToken)
}

func TestAuthenticatedTokenSourceRejectedCredentialRunsInteractiveAuth(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant"}`)
	}))
	defer tokenServer.Close()

	conf := testOAuthConfig(tokenServer.URL)
	stored := &oauth2.Token{AccessToken: "access", RefreshToken: "revoked", Expiry: time.Now().Add(time.Hour)}
	interactiveToken := &oauth2.Token{AccessToken: "interactive-access", RefreshToken: "interactive-refresh", Expiry: time.Now().Add(time.Hour)}
	interactiveCalls := 0
	src, err := authenticatedTokenSource(context.Background(), conf, stored, filepath.Join(t.TempDir(), "token.json"), func(context.Context, *oauth2.Config) (*oauth2.Token, error) {
		interactiveCalls++
		return interactiveToken, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, interactiveCalls)

	tok, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, interactiveToken, tok)
}

func TestAuthenticatedTokenSourceMissingRefreshTokenRunsInteractiveAuth(t *testing.T) {
	var requests atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "the token endpoint should not be contacted", http.StatusInternalServerError)
	}))
	defer tokenServer.Close()

	conf := testOAuthConfig(tokenServer.URL)
	stored := &oauth2.Token{AccessToken: "access", Expiry: time.Now().Add(time.Hour)}
	interactiveToken := &oauth2.Token{AccessToken: "interactive-access", RefreshToken: "interactive-refresh", Expiry: time.Now().Add(time.Hour)}
	interactiveCalls := 0
	src, err := authenticatedTokenSource(context.Background(), conf, stored, filepath.Join(t.TempDir(), "token.json"), func(context.Context, *oauth2.Config) (*oauth2.Token, error) {
		interactiveCalls++
		return interactiveToken, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, interactiveCalls)
	assert.Zero(t, requests.Load(), "a token without a refresh token cannot be refreshed; go straight to interactive auth")

	tok, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, interactiveToken, tok)
}

func TestAuthenticatedTokenSourceTransientRefreshFailureDoesNotRunInteractiveAuth(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary outage", http.StatusServiceUnavailable)
	}))
	defer tokenServer.Close()

	conf := testOAuthConfig(tokenServer.URL)
	stored := &oauth2.Token{AccessToken: "access", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}
	interactiveCalls := 0
	_, err := authenticatedTokenSource(context.Background(), conf, stored, filepath.Join(t.TempDir(), "token.json"), func(context.Context, *oauth2.Config) (*oauth2.Token, error) {
		interactiveCalls++
		return nil, nil
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "could not refresh Google credentials")
	assert.Zero(t, interactiveCalls)
}

func TestAuthenticatedTokenSourceTimesOutStoredCredentialRefresh(t *testing.T) {
	var requests atomic.Int32
	tokenClient := &http.Client{
		Timeout: 20 * time.Millisecond,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests.Add(1)
			<-req.Context().Done()
			return nil, req.Context().Err()
		}),
	}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, tokenClient)
	conf := testOAuthConfig("https://tokens.example.test")
	stored := &oauth2.Token{AccessToken: "access", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}
	interactiveCalls := 0

	_, err := authenticatedTokenSource(ctx, conf, stored, filepath.Join(t.TempDir(), "token.json"), func(context.Context, *oauth2.Config) (*oauth2.Token, error) {
		interactiveCalls++
		return nil, nil
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ErrorContains(t, err, "OAuth token request timed out")
	assert.ErrorContains(t, err, "check the network connection and try again")
	assert.Equal(t, int32(1), requests.Load(), "timed-out token POSTs must not be retried automatically")
	assert.Zero(t, interactiveCalls, "a timeout must not trigger interactive reauthentication")
}

func TestAuthenticatedTokenSourcePersistsWithoutRefreshingNewInteractiveToken(t *testing.T) {
	var requests atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"unexpected","token_type":"Bearer","expires_in":3600}`)
	}))
	defer tokenServer.Close()

	conf := testOAuthConfig(tokenServer.URL)
	interactiveToken := &oauth2.Token{AccessToken: "interactive-access", RefreshToken: "interactive-refresh", Expiry: time.Now().Add(time.Hour)}
	path := filepath.Join(t.TempDir(), "token.json")
	src, err := authenticatedTokenSource(context.Background(), conf, nil, path, func(context.Context, *oauth2.Config) (*oauth2.Token, error) {
		return interactiveToken, nil
	})
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var saved oauth2.Token
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, interactiveToken.AccessToken, saved.AccessToken)
	assert.Equal(t, interactiveToken.RefreshToken, saved.RefreshToken)

	tok, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, interactiveToken, tok)
	assert.Zero(t, requests.Load())
}

func TestAuthenticatedTokenSourceRetriesInteractiveTokenSaveAfterFailure(t *testing.T) {
	tokenDir := filepath.Join(t.TempDir(), "blocked")
	path := filepath.Join(tokenDir, "token.json")
	require.NoError(t, os.WriteFile(tokenDir, []byte("not a directory"), 0600))

	conf := testOAuthConfig("http://127.0.0.1/unused")
	interactiveToken := &oauth2.Token{
		AccessToken:  "interactive-access",
		RefreshToken: "interactive-refresh",
		Expiry:       time.Now().Add(time.Hour),
	}
	interactiveCalls := 0
	src, err := authenticatedTokenSource(context.Background(), conf, nil, path, func(context.Context, *oauth2.Config) (*oauth2.Token, error) {
		interactiveCalls++
		return interactiveToken, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, interactiveCalls)
	_, err = os.Stat(path)
	assert.Error(t, err, "token file should not exist after the initial failed save")

	require.NoError(t, os.Remove(tokenDir))
	require.NoError(t, os.Mkdir(tokenDir, 0700))
	tok, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, interactiveToken, tok)
	assert.Equal(t, 1, interactiveCalls, "retrying persistence must not repeat interactive auth")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var saved oauth2.Token
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, interactiveToken.AccessToken, saved.AccessToken)
	assert.Equal(t, interactiveToken.RefreshToken, saved.RefreshToken)
}

func TestAuthenticatedTokenSourceRefreshesAgainDuringLongRun(t *testing.T) {
	var requests atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestNumber := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if requestNumber == 1 {
			// x/oauth2's safety margin considers this token expired before the next request.
			fmt.Fprint(w, `{"access_token":"short-lived","token_type":"Bearer","expires_in":1}`)
			return
		}
		fmt.Fprint(w, `{"access_token":"renewed-during-run","token_type":"Bearer","expires_in":3600}`)
	}))
	defer tokenServer.Close()

	conf := testOAuthConfig(tokenServer.URL)
	path := filepath.Join(t.TempDir(), "token.json")
	stored := &oauth2.Token{AccessToken: "stored", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}
	src, err := authenticatedTokenSource(context.Background(), conf, stored, path, func(context.Context, *oauth2.Config) (*oauth2.Token, error) {
		return nil, errors.New("interactive auth should not run")
	})
	require.NoError(t, err)

	tok, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, "renewed-during-run", tok.AccessToken)
	assert.Equal(t, int32(2), requests.Load())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var saved oauth2.Token
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, tok.AccessToken, saved.AccessToken)
}

func TestAuthenticatedTokenSourceTimesOutLaterRefresh(t *testing.T) {
	var requests atomic.Int32
	tokenClient := &http.Client{
		Timeout: 20 * time.Millisecond,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if requests.Add(1) == 1 {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"access_token":"short-lived","token_type":"Bearer","expires_in":1}`)),
					Request:    req,
				}, nil
			}
			<-req.Context().Done()
			return nil, req.Context().Err()
		}),
	}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, tokenClient)
	conf := testOAuthConfig("https://tokens.example.test")
	stored := &oauth2.Token{AccessToken: "stored", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}
	src, err := authenticatedTokenSource(ctx, conf, stored, filepath.Join(t.TempDir(), "token.json"), func(context.Context, *oauth2.Config) (*oauth2.Token, error) {
		return nil, errors.New("interactive auth should not run")
	})
	require.NoError(t, err)

	_, err = src.Token()
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ErrorContains(t, err, "OAuth token request timed out")
	assert.Equal(t, int32(2), requests.Load(), "the later timed-out refresh must not be retried automatically")
}

func testOAuthConfig(tokenURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		Endpoint: oauth2.Endpoint{
			AuthURL:   "https://accounts.example.test/auth",
			TokenURL:  tokenURL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

func TestAuthCallbackHandler(t *testing.T) {
	const state = "expected-state"

	serve := func(t *testing.T, target string) (*httptest.ResponseRecorder, chan authCallbackResult) {
		t.Helper()
		resultCh := make(chan authCallbackResult, 1)
		w := httptest.NewRecorder()
		authCallbackHandler(state, resultCh)(w, httptest.NewRequest("GET", target, nil))
		return w, resultCh
	}

	t.Run("valid state and code delivers code", func(t *testing.T) {
		w, resultCh := serve(t, "/?code=auth-code&state="+state)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "Camflow Authorization Received")
		assert.NotContains(t, w.Body.String(), "Authentication Successful")
		require.Len(t, resultCh, 1)
		result := <-resultCh
		assert.NoError(t, result.err)
		assert.Equal(t, "auth-code", result.code)
	})

	t.Run("mismatched state is rejected and keeps waiting", func(t *testing.T) {
		w, resultCh := serve(t, "/?code=auth-code&state=forged")
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Empty(t, resultCh)
	})

	t.Run("missing code is rejected and keeps waiting", func(t *testing.T) {
		w, resultCh := serve(t, "/favicon.ico")
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Empty(t, resultCh)
	})

	t.Run("denial delivers error", func(t *testing.T) {
		w, resultCh := serve(t, "/?error=access_denied&state="+state)
		assert.Equal(t, http.StatusOK, w.Code)
		require.Len(t, resultCh, 1)
		assert.ErrorContains(t, (<-resultCh).err, "access_denied")
	})

	t.Run("denial includes error_description when present", func(t *testing.T) {
		w, resultCh := serve(t, "/?error=access_denied&error_description=policy+forbids+it&state="+state)
		assert.Equal(t, http.StatusOK, w.Code)
		require.Len(t, resultCh, 1)
		assert.ErrorContains(t, (<-resultCh).err, "access_denied: policy forbids it")
	})

	t.Run("denial with mismatched state is rejected and keeps waiting", func(t *testing.T) {
		w, resultCh := serve(t, "/?error=access_denied&state=forged")
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Empty(t, resultCh)
	})

	t.Run("denial with missing state is rejected and keeps waiting", func(t *testing.T) {
		w, resultCh := serve(t, "/?error=access_denied")
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Empty(t, resultCh)
	})

	t.Run("first valid callback wins", func(t *testing.T) {
		resultCh := make(chan authCallbackResult, 1)
		handler := authCallbackHandler(state, resultCh)
		handler(httptest.NewRecorder(), httptest.NewRequest("GET", "/?code=first&state="+state, nil))
		handler(httptest.NewRecorder(), httptest.NewRequest("GET", "/?error=access_denied&state="+state, nil))

		require.Len(t, resultCh, 1)
		result := <-resultCh
		assert.NoError(t, result.err)
		assert.Equal(t, "first", result.code)
	})
}

func TestGetAuthenticatedGooglePhotosClientRejectsInvalidRedirectBeforeCacheChanges(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	cfg := config.CamflowConfig{GooglePhotos: config.GooglePhotosConfig{
		ClientId:     "client-id",
		ClientSecret: "client-secret",
		RedirectURI:  "http://0.0.0.0:8080",
	}}

	client, err := GetAuthenticatedGooglePhotosClient(context.Background(), cfg, cacheDir)
	require.Error(t, err)
	assert.Nil(t, client)
	assert.ErrorContains(t, err, "loopback")
	_, statErr := os.Stat(cacheDir)
	assert.True(t, os.IsNotExist(statErr), "invalid redirect URI must fail before creating the cache directory")
}

func TestGetAuthenticatedGooglePhotosClientNormalizesRedirectForDirectCall(t *testing.T) {
	tests := []struct {
		name        string
		redirectURI string
	}{
		{name: "empty", redirectURI: ""},
		{name: "legacy OOB", redirectURI: "urn:ietf:wg:oauth:2.0:oob"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cacheDir := filepath.Join(t.TempDir(), "cache")
			cfg := config.CamflowConfig{GooglePhotos: config.GooglePhotosConfig{
				ClientId:     "client-id",
				ClientSecret: "client-secret",
				RedirectURI:  tt.redirectURI,
			}}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			client, err := GetAuthenticatedGooglePhotosClient(ctx, cfg, cacheDir)
			require.ErrorIs(t, err, context.Canceled)
			assert.Nil(t, client)
			assert.Equal(t, tt.redirectURI, cfg.GooglePhotos.RedirectURI, "normalization must not mutate the caller's config")
			info, statErr := os.Stat(cacheDir)
			require.NoError(t, statErr)
			assert.True(t, info.IsDir(), "normalized redirect should advance beyond validation")
		})
	}
}

func TestGetTokenFromWebRejectsInvalidRedirectBeforeOpeningBrowser(t *testing.T) {
	conf := testOAuthConfig("http://127.0.0.1/unused")
	conf.RedirectURL = "localhost:8080"
	browserOpened := make(chan struct{}, 1)

	_, err := getTokenFromWebWithBrowser(context.Background(), conf, func(string) {
		browserOpened <- struct{}{}
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "http scheme")
	select {
	case <-browserOpened:
		t.Fatal("browser opener must not run for an invalid redirect URI")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestGetTokenFromWebPKCE(t *testing.T) {
	addr := reserveLoopbackAddress(t)
	tokenFormCh := make(chan url.Values, 1)
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		tokenFormCh <- r.Form
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"access","refresh_token":"refresh","token_type":"Bearer","expires_in":3600}`)
	}))
	defer tokenServer.Close()

	conf := testOAuthConfig(tokenServer.URL)
	conf.RedirectURL = "http://" + addr
	callbackBodyCh := make(chan string, 1)
	authQueryCh := make(chan url.Values, 1)
	token, err := getTokenFromWebWithBrowser(context.Background(), conf, func(authURL string) {
		u, parseErr := url.Parse(authURL)
		if parseErr != nil {
			callbackBodyCh <- "parse error: " + parseErr.Error()
			return
		}
		authQueryCh <- u.Query()
		callbackURL := conf.RedirectURL + "?code=auth-code&state=" + url.QueryEscape(u.Query().Get("state"))
		response, requestErr := (&http.Client{Timeout: 2 * time.Second}).Get(callbackURL)
		if requestErr != nil {
			callbackBodyCh <- "request error: " + requestErr.Error()
			return
		}
		defer response.Body.Close()
		body, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			callbackBodyCh <- "read error: " + readErr.Error()
			return
		}
		callbackBodyCh <- string(body)
	})
	require.NoError(t, err)
	assert.Equal(t, "access", token.AccessToken)
	assert.Equal(t, "refresh", token.RefreshToken)
	assert.Contains(t, <-callbackBodyCh, "Camflow Authorization Received")

	authQuery := <-authQueryCh
	assert.Equal(t, "S256", authQuery.Get("code_challenge_method"))
	assert.NotEmpty(t, authQuery.Get("code_challenge"))
	tokenForm := <-tokenFormCh
	assert.Equal(t, "auth-code", tokenForm.Get("code"))
	assert.NotEmpty(t, tokenForm.Get("code_verifier"))
	assertLoopbackListenerClosed(t, addr)
}

func TestGetTokenFromWebGracefulShutdown(t *testing.T) {
	t.Run("listener closes before token exchange finishes", func(t *testing.T) {
		addr := reserveLoopbackAddress(t)
		exchangeStartedCh := make(chan struct{}, 1)
		releaseExchangeCh := make(chan struct{})
		var releaseExchangeOnce atomic.Bool
		releaseExchange := func() {
			if releaseExchangeOnce.CompareAndSwap(false, true) {
				close(releaseExchangeCh)
			}
		}
		defer releaseExchange()

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			exchangeStartedCh <- struct{}{}
			<-releaseExchangeCh
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"access","refresh_token":"refresh","token_type":"Bearer","expires_in":3600}`)
		}))
		defer tokenServer.Close()

		conf := testOAuthConfig(tokenServer.URL)
		conf.RedirectURL = "http://" + addr
		callbackBodyCh := make(chan string, 1)
		type flowResult struct {
			token *oauth2.Token
			err   error
		}
		flowResultCh := make(chan flowResult, 1)
		go func() {
			token, err := getTokenFromWebWithBrowser(context.Background(), conf, func(authURL string) {
				u, parseErr := url.Parse(authURL)
				if parseErr != nil {
					callbackBodyCh <- "parse error: " + parseErr.Error()
					return
				}
				callbackURL := conf.RedirectURL + "?code=auth-code&state=" + url.QueryEscape(u.Query().Get("state"))
				response, requestErr := (&http.Client{Timeout: 2 * time.Second}).Get(callbackURL)
				if requestErr != nil {
					callbackBodyCh <- "request error: " + requestErr.Error()
					return
				}
				defer response.Body.Close()
				body, readErr := io.ReadAll(response.Body)
				if readErr != nil {
					callbackBodyCh <- "read error: " + readErr.Error()
					return
				}
				callbackBodyCh <- string(body)
			})
			flowResultCh <- flowResult{token: token, err: err}
		}()

		select {
		case body := <-callbackBodyCh:
			assert.Contains(t, body, "Camflow Authorization Received")
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for browser callback response")
		}
		select {
		case <-exchangeStartedCh:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for token exchange")
		}
		assertLoopbackListenerClosed(t, addr)

		releaseExchange()
		select {
		case result := <-flowResultCh:
			require.NoError(t, result.err)
			require.NotNil(t, result.token)
			assert.Equal(t, "access", result.token.AccessToken)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for OAuth flow to finish")
		}
	})

	t.Run("exchange failure is not reported as browser success", func(t *testing.T) {
		addr := reserveLoopbackAddress(t)
		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_client"}`)
		}))
		defer tokenServer.Close()

		conf := testOAuthConfig(tokenServer.URL)
		conf.RedirectURL = "http://" + addr
		callbackBodyCh := make(chan string, 1)
		_, err := getTokenFromWebWithBrowser(context.Background(), conf, func(authURL string) {
			u, parseErr := url.Parse(authURL)
			if parseErr != nil {
				callbackBodyCh <- "parse error: " + parseErr.Error()
				return
			}
			callbackURL := conf.RedirectURL + "?code=auth-code&state=" + url.QueryEscape(u.Query().Get("state"))
			response, requestErr := (&http.Client{Timeout: 2 * time.Second}).Get(callbackURL)
			if requestErr != nil {
				callbackBodyCh <- "request error: " + requestErr.Error()
				return
			}
			defer response.Body.Close()
			body, readErr := io.ReadAll(response.Body)
			if readErr != nil {
				callbackBodyCh <- "read error: " + readErr.Error()
				return
			}
			callbackBodyCh <- string(body)
		})
		require.Error(t, err)
		assert.ErrorContains(t, err, "unable to retrieve token from web exchange")
		select {
		case body := <-callbackBodyCh:
			assert.Contains(t, body, "Camflow Authorization Received")
			assert.NotContains(t, body, "Authentication Successful")
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for browser callback response")
		}
		assertLoopbackListenerClosed(t, addr)
	})

	t.Run("denial response is flushed and listener closes", func(t *testing.T) {
		addr := reserveLoopbackAddress(t)
		conf := testOAuthConfig("http://127.0.0.1/unused")
		conf.RedirectURL = "http://" + addr
		callbackBodyCh := make(chan string, 1)
		_, err := getTokenFromWebWithBrowser(context.Background(), conf, func(authURL string) {
			u, parseErr := url.Parse(authURL)
			if parseErr != nil {
				callbackBodyCh <- "parse error: " + parseErr.Error()
				return
			}
			callbackURL := conf.RedirectURL + "?error=access_denied&state=" + url.QueryEscape(u.Query().Get("state"))
			response, requestErr := (&http.Client{Timeout: 2 * time.Second}).Get(callbackURL)
			if requestErr != nil {
				callbackBodyCh <- "request error: " + requestErr.Error()
				return
			}
			defer response.Body.Close()
			body, readErr := io.ReadAll(response.Body)
			if readErr != nil {
				callbackBodyCh <- "read error: " + readErr.Error()
				return
			}
			callbackBodyCh <- string(body)
		})
		require.Error(t, err)
		assert.ErrorContains(t, err, "access_denied")
		assert.Contains(t, <-callbackBodyCh, "Camflow Authentication Failed")
		assertLoopbackListenerClosed(t, addr)
	})

	t.Run("pre-canceled context does not open browser and leaves listener closed", func(t *testing.T) {
		addr := reserveLoopbackAddress(t)
		conf := testOAuthConfig("http://127.0.0.1/unused")
		conf.RedirectURL = "http://" + addr
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		browserOpenedCh := make(chan struct{}, 1)

		_, err := getTokenFromWebWithBrowser(ctx, conf, func(string) {
			browserOpenedCh <- struct{}{}
		})
		require.ErrorIs(t, err, context.Canceled)
		select {
		case <-browserOpenedCh:
			t.Fatal("browser opener must not run for a pre-canceled context")
		case <-time.After(50 * time.Millisecond):
		}
		assertLoopbackListenerClosed(t, addr)
	})
}

func reserveLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())
	return addr
}

func assertLoopbackListenerClosed(t *testing.T, addr string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if conn != nil {
		conn.Close()
	}
	assert.Error(t, err, "OAuth callback listener should be closed")
}
