package handlers

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"spotter/ent"
	"spotter/ent/user"
	"spotter/internal/views/auth"
)

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if h.GetUser(r.Context()) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	h.Render(w, r, auth.Login())
}

func (h *Handler) PostLogin(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "" || password == "" {
		http.Error(w, "Username and password required", http.StatusBadRequest)
		return
	}

	// 1. Authenticate against Navidrome
	if err := h.authenticateNavidrome(username, password); err != nil {
		h.Logger.Error("Navidrome authentication failed", "error", err)
		// Return error to HTMX
		w.Header().Set("HX-Retarget", "body") // Optionally retarget to show error
		http.Error(w, "Invalid credentials or Navidrome error", http.StatusUnauthorized)
		return
	}

	// 2. Create or Update User
	u, err := h.Client.User.Query().
		Where(user.Username(username)).
		WithNavidromeAuth().
		Only(r.Context())

	if err != nil {
		if !ent.IsNotFound(err) {
			h.Logger.Error("Failed to query user", "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		// Create
		u, err = h.Client.User.Create().
			SetUsername(username).
			SetLastLoginAt(time.Now()).
			Save(r.Context())
		if err != nil {
			h.Logger.Error("Failed to create user", "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		// Create Navidrome Auth
		_, err = h.Client.NavidromeAuth.Create().
			SetUser(u).
			SetPassword(password).
			Save(r.Context())
		if err != nil {
			h.Logger.Error("Failed to create navidrome auth", "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	} else {
		// Update
		u, err = u.Update().
			SetLastLoginAt(time.Now()).
			Save(r.Context())
		if err != nil {
			h.Logger.Error("Failed to update user", "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		// Update Navidrome Auth
		if u.Edges.NavidromeAuth != nil {
			_, err = h.Client.NavidromeAuth.UpdateOne(u.Edges.NavidromeAuth).
				SetPassword(password).
				Save(r.Context())
		} else {
			_, err = h.Client.NavidromeAuth.Create().
				SetUser(u).
				SetPassword(password).
				Save(r.Context())
		}
		if err != nil {
			h.Logger.Error("Failed to save navidrome auth", "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	}

	// Trigger initial sync
	go func(user *ent.User) {
		if err := h.Syncer.Sync(context.Background(), user); err != nil {
			h.Logger.Error("failed to sync user data", "error", err)
		}
	}(u)

	// 3. Set Session Cookie (Simple implementation for MVP)
	http.SetCookie(w, &http.Cookie{
		Name:     "spotter_user",
		Value:    u.Username,
		Path:     "/",
		HttpOnly: true,
		Expires:  time.Now().Add(24 * time.Hour),
		SameSite: http.SameSiteLaxMode,
	})

	w.Header().Set("HX-Redirect", "/")
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "spotter_user",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Expires:  time.Now().Add(-1 * time.Hour),
	})
	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}

func (h *Handler) authenticateNavidrome(username, password string) error {
	baseURL := h.Config.Navidrome.BaseURL
	if baseURL == "" {
		// If base URL is not set, we might want to fail or allow bypass for dev?
		// For this strict requirement, we fail.
		return fmt.Errorf("navidrome base url not configured")
	}

	salt := "spotter" // In production, generate random
	hash := md5.New()
	hash.Write([]byte(password + salt))
	token := hex.EncodeToString(hash.Sum(nil))

	params := url.Values{}
	params.Set("u", username)
	params.Set("t", token)
	params.Set("s", salt)
	params.Set("v", "1.16.1")
	params.Set("c", "spotter")
	params.Set("f", "json")

	// Handle trailing slash in base URL
	apiURL := fmt.Sprintf("%s/rest/ping.view?%s", baseURL, params.Encode())
	// Simple check if base URL already has /rest
	// But usually baseURL is just the host:port or host:port/navidrome

	resp, err := http.Get(apiURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("navidrome returned status: %d", resp.StatusCode)
	}

	var result struct {
		SubsonicResponse struct {
			Status string `json:"status"`
			Error  struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"subsonic-response"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	if result.SubsonicResponse.Status != "ok" {
		return fmt.Errorf("navidrome error: %s", result.SubsonicResponse.Error.Message)
	}

	return nil
}
