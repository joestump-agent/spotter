package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"spotter/ent"
	"spotter/ent/user"
	"spotter/internal/config"
	"spotter/internal/events"
	"spotter/internal/handlers"
	"spotter/internal/services"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestDB(t *testing.T) *ent.Client {
	client, err := ent.Open("sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	require.NoError(t, err)
	require.NoError(t, client.Schema.Create(context.Background()))
	t.Cleanup(func() {
		client.Close()
	})
	return client
}

func TestLogin_Get(t *testing.T) {
	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus)
	h := handlers.New(client, cfg, logger, syncer, nil, nil, nil, nil, bus)

	req := httptest.NewRequest("GET", "/auth/login", nil)
	w := httptest.NewRecorder()

	h.Login(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "Spotter uses your Navidrome credentials")
	assert.Contains(t, string(body), "Log in with Navidrome")
}

func TestPostLogin_Success(t *testing.T) {
	// 1. Mock Navidrome Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/rest/ping.view", r.URL.Path)
		assert.Equal(t, "testuser", r.URL.Query().Get("u"))

		w.Header().Set("Content-Type", "application/json")
		response := map[string]interface{}{
			"subsonic-response": map[string]interface{}{
				"status":  "ok",
				"version": "1.16.1",
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	// 2. Setup
	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = ts.URL
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus)
	h := handlers.New(client, cfg, logger, syncer, nil, nil, nil, nil, bus)

	// 3. Request
	form := url.Values{}
	form.Set("username", "testuser")
	form.Set("password", "secret123")
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// 4. Execute
	h.PostLogin(w, req)

	// 5. Assert Response
	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "/", w.Header().Get("HX-Redirect"))

	// Check Cookies
	cookies := resp.Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, "spotter_user", cookies[0].Name)
	assert.Equal(t, "testuser", cookies[0].Value)

	// 6. Assert Database State
	u, err := client.User.Query().
		Where(user.Username("testuser")).
		WithNavidromeAuth().
		Only(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, u)
	assert.NotNil(t, u.Edges.NavidromeAuth)
	assert.Equal(t, "secret123", u.Edges.NavidromeAuth.Password)
}

func TestPostLogin_InvalidCredentials(t *testing.T) {
	// 1. Mock Navidrome Server to return error
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		response := map[string]interface{}{
			"subsonic-response": map[string]interface{}{
				"status": "failed",
				"error": map[string]interface{}{
					"code":    40,
					"message": "Wrong username or password",
				},
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	// 2. Setup
	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = ts.URL
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus)
	h := handlers.New(client, cfg, logger, syncer, nil, nil, nil, nil, bus)

	// 3. Request
	form := url.Values{}
	form.Set("username", "testuser")
	form.Set("password", "wrongpassword")
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// 4. Execute
	h.PostLogin(w, req)

	// 5. Assert
	assert.Equal(t, http.StatusUnauthorized, w.Result().StatusCode)

	// User should not exist
	exists, _ := client.User.Query().Where(user.Username("testuser")).Exist(context.Background())
	assert.False(t, exists)
}
