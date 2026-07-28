package larkagent_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	serviceim "github.com/liuchong/lark-agent/internal/lark"
)

type memoryUserTokenStore struct {
	mu     sync.Mutex
	tokens serviceim.UserTokens
	stored []serviceim.UserTokens
}

func (s *memoryUserTokenStore) LoadUserTokens(context.Context) (serviceim.UserTokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens, nil
}

func (s *memoryUserTokenStore) StoreUserTokens(_ context.Context, tokens serviceim.UserTokens) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = tokens
	s.stored = append(s.stored, tokens)
	return nil
}

func TestExpiredUserTokenRefreshesPersistsAndReplaysOnce(t *testing.T) {
	var mu sync.Mutex
	apiCalls := 0
	refreshCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/test":
			mu.Lock()
			apiCalls++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if r.Header.Get("Authorization") != "Bearer fresh-access-token" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":99991668,"msg":"Authentication token expired. Please request a new one."}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"ok":true}}`))
		case "/oauth/v3/token":
			mu.Lock()
			refreshCalls++
			mu.Unlock()
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode refresh request: %v", err)
			}
			if body["grant_type"] != "refresh_token" || body["refresh_token"] != "old-refresh-token" {
				t.Errorf("refresh body=%v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"code":0,
				"access_token":"fresh-access-token",
				"refresh_token":"fresh-refresh-token",
				"expires_in":7200,
				"refresh_token_expires_in":604800
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	store := &memoryUserTokenStore{
		tokens: serviceim.UserTokens{
			AccessToken:  "expired-access-token",
			RefreshToken: "old-refresh-token",
		},
	}
	client, err := serviceim.NewClient(serviceim.ClientConfig{
		AppID:           "cli_test",
		AppSecret:       "app-secret",
		UserAccessToken: "expired-access-token",
		RefreshToken:    "old-refresh-token",
		BaseURL:         server.URL,
		OAuthBaseURL:    server.URL,
		UserTokenStore:  store,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.CallAPI(context.Background(), serviceim.APIRequest{
		Method: http.MethodGet,
		Path:   "/open-apis/test",
		As:     serviceim.IdentityUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("missing result")
	}
	mu.Lock()
	gotAPICalls, gotRefreshCalls := apiCalls, refreshCalls
	mu.Unlock()
	if gotAPICalls != 2 || gotRefreshCalls != 1 {
		t.Fatalf("api_calls=%d refresh_calls=%d", gotAPICalls, gotRefreshCalls)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.stored) != 1 ||
		store.stored[0].AccessToken != "fresh-access-token" ||
		store.stored[0].RefreshToken != "fresh-refresh-token" {
		t.Fatalf("stored=%+v", store.stored)
	}
}

func TestExpiredCachedUserTokenReloadsNewerStoredTokenBeforeRefresh(t *testing.T) {
	var mu sync.Mutex
	apiCalls := 0
	refreshCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/test":
			mu.Lock()
			apiCalls++
			mu.Unlock()
			if r.Header.Get("Authorization") != "Bearer newer-access-token" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":99991668,"msg":"Authentication token expired. Please request a new one."}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"ok":true}}`))
		case "/oauth/v3/token":
			mu.Lock()
			refreshCalls++
			mu.Unlock()
			http.Error(w, "refresh should not be called", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	store := &memoryUserTokenStore{
		tokens: serviceim.UserTokens{
			AccessToken:  "newer-access-token",
			RefreshToken: "newer-refresh-token",
		},
	}
	client, err := serviceim.NewClient(serviceim.ClientConfig{
		AppID:           "cli_test",
		AppSecret:       "app-secret",
		UserAccessToken: "expired-cached-token",
		RefreshToken:    "old-refresh-token",
		BaseURL:         server.URL,
		OAuthBaseURL:    server.URL,
		UserTokenStore:  store,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallAPI(context.Background(), serviceim.APIRequest{
		Method: http.MethodGet,
		Path:   "/open-apis/test",
		As:     serviceim.IdentityUser,
	}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if apiCalls != 2 || refreshCalls != 0 {
		t.Fatalf("api_calls=%d refresh_calls=%d", apiCalls, refreshCalls)
	}
}

func TestConcurrentExpiredUserRequestsConsumeRefreshTokenOnce(t *testing.T) {
	var mu sync.Mutex
	refreshCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/test":
			if r.Header.Get("Authorization") != "Bearer concurrent-access-token" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":99991668,"msg":"Authentication token expired. Please request a new one."}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"ok":true}}`))
		case "/oauth/v3/token":
			mu.Lock()
			refreshCalls++
			call := refreshCalls
			mu.Unlock()
			if call != 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":20036,"error":"invalid_grant","error_description":"refresh token already used"}`))
				return
			}
			_, _ = w.Write([]byte(`{
				"code":0,
				"access_token":"concurrent-access-token",
				"refresh_token":"concurrent-refresh-token",
				"expires_in":7200,
				"refresh_token_expires_in":604800
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	store := &memoryUserTokenStore{
		tokens: serviceim.UserTokens{
			AccessToken:  "expired-access-token",
			RefreshToken: "single-use-refresh-token",
		},
	}
	client, err := serviceim.NewClient(serviceim.ClientConfig{
		AppID:           "cli_test",
		AppSecret:       "app-secret",
		UserAccessToken: "expired-access-token",
		RefreshToken:    "single-use-refresh-token",
		BaseURL:         server.URL,
		OAuthBaseURL:    server.URL,
		UserTokenStore:  store,
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var callers sync.WaitGroup
	for range 2 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			<-start
			_, callErr := client.CallAPI(context.Background(), serviceim.APIRequest{
				Method: http.MethodGet,
				Path:   "/open-apis/test",
				As:     serviceim.IdentityUser,
			})
			errs <- callErr
		}()
	}
	close(start)
	callers.Wait()
	close(errs)
	for callErr := range errs {
		if callErr != nil {
			t.Fatal(callErr)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if refreshCalls != 1 {
		t.Fatalf("refresh_calls=%d", refreshCalls)
	}
}

func TestExpiredUserTokenRefreshFailureDoesNotReplayOriginalRequest(t *testing.T) {
	var mu sync.Mutex
	apiCalls := 0
	refreshCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/open-apis/test":
			apiCalls++
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":99991668,"msg":"Authentication token expired. Please request a new one."}`))
		case "/oauth/v3/token":
			refreshCalls++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":20037,"error":"invalid_grant","error_description":"refresh token expired"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	store := &memoryUserTokenStore{
		tokens: serviceim.UserTokens{
			AccessToken:  "expired-access-token",
			RefreshToken: "expired-refresh-token",
		},
	}
	client, err := serviceim.NewClient(serviceim.ClientConfig{
		AppID:           "cli_test",
		AppSecret:       "app-secret",
		UserAccessToken: "expired-access-token",
		RefreshToken:    "expired-refresh-token",
		BaseURL:         server.URL,
		OAuthBaseURL:    server.URL,
		UserTokenStore:  store,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallAPI(context.Background(), serviceim.APIRequest{
		Method: http.MethodGet,
		Path:   "/open-apis/test",
		As:     serviceim.IdentityUser,
	}); err == nil {
		t.Fatal("expired refresh token was accepted")
	}
	mu.Lock()
	defer mu.Unlock()
	if apiCalls != 1 || refreshCalls != 1 {
		t.Fatalf("api_calls=%d refresh_calls=%d", apiCalls, refreshCalls)
	}
}
