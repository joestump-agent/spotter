// Governing: SPEC music-provider-integration, SPEC listen-playlist-sync
package spotify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"spotter/ent"
	"spotter/internal/config"
	"spotter/internal/httputil"
	"spotter/internal/providers"
	"spotter/internal/resilience"

	"golang.org/x/oauth2"
	spotifyOAuth "golang.org/x/oauth2/spotify"
)

// Spotify OAuth scopes needed for our functionality
var scopes = []string{
	"user-read-recently-played",
	"user-read-private",
	"playlist-read-private",
	"playlist-modify-public",
	"playlist-modify-private",
}

const (
	// defaultBaseURL is the Spotify Web API base URL. Overridable in tests.
	defaultBaseURL = "https://api.spotify.com"

	// Governing: SPEC music-provider-integration REQ-PROV-033 (cursor pagination page cap)
	// maxRecentlyPlayedPages bounds the before-cursor pagination loop.
	maxRecentlyPlayedPages = 10

	// Governing: SPEC music-provider-integration REQ-PROV-030 (Spotify add-tracks batching)
	// addTracksBatchSize is Spotify's maximum number of track URIs per add-tracks request.
	addTracksBatchSize = 100
)

type Provider struct {
	logger *slog.Logger
	config *config.Config
	user   *ent.User
	auth   *ent.SpotifyAuth
	oauth  *oauth2.Config
	// db is used to persist refreshed tokens back to SpotifyAuth.
	// May be nil (e.g. authenticator-only instances); persistence is then skipped.
	db         *ent.Client
	baseURL    string
	httpClient *http.Client
}

// Governing: SPEC music-provider-integration REQ-PROV-030 (Spotify: HistoryFetcher + PlaylistManager + Authenticator)
var _ providers.HistoryFetcher = (*Provider)(nil)
var _ providers.PlaylistManager = (*Provider)(nil)
var _ providers.Authenticator = (*Provider)(nil)

// Governing: ADR-0016 (pluggable provider factory), SPEC music-provider-integration REQ-PROV-011 (nil,nil if unconfigured),
// REQ-PROV-012 (credentials from user.Edges.SpotifyAuth)
// New returns a factory that creates Spotify providers for a given user.
// The ent client is used to persist refreshed OAuth tokens (encryption is
// handled by the database hooks registered on the client).
func New(logger *slog.Logger, cfg *config.Config, db *ent.Client) providers.Factory {
	return func(ctx context.Context, user *ent.User) (providers.Provider, error) {
		// Check if the user has Spotify authentication data.
		// We expect the caller to have loaded the edges (e.g. WithSpotifyAuth()).
		if user.Edges.SpotifyAuth == nil {
			return nil, nil
		}

		return &Provider{
			logger:     logger,
			config:     cfg,
			user:       user,
			auth:       user.Edges.SpotifyAuth,
			oauth:      newOAuthConfig(cfg),
			db:         db,
			baseURL:    defaultBaseURL,
			httpClient: &http.Client{Timeout: 30 * time.Second},
		}, nil
	}
}

// NewAuthenticator returns an authenticator factory for Spotify.
// This is used for the OAuth flow before a user is connected.
func NewAuthenticator(logger *slog.Logger, cfg *config.Config) providers.AuthenticatorFactory {
	return func() providers.Authenticator {
		return &Provider{
			logger:     logger,
			config:     cfg,
			oauth:      newOAuthConfig(cfg),
			baseURL:    defaultBaseURL,
			httpClient: &http.Client{Timeout: 30 * time.Second},
		}
	}
}

// Governing: SPEC music-provider-integration REQ-PROV-031 (OAuth2 via golang.org/x/oauth2, configurable redirect URI)
// Sanctioned deviation: REQ-PROV-031 calls for the PKCE flow, but this
// self-hosted deployment keeps the authorization-code + client-secret flow
// (the client secret never leaves the server). The spec amendment sanctioning
// this is pending; do not switch to PKCE without revisiting that decision.
func newOAuthConfig(cfg *config.Config) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     cfg.Spotify.ClientID,
		ClientSecret: cfg.Spotify.ClientSecret,
		RedirectURL:  cfg.Spotify.RedirectURL,
		Scopes:       scopes,
		Endpoint:     spotifyOAuth.Endpoint,
	}
}

func (p *Provider) Type() providers.Type {
	return providers.TypeSpotify
}

// SupportsAuth returns true since Spotify supports OAuth authentication from preferences.
func (p *Provider) SupportsAuth() bool {
	return true
}

// GetAuthURL returns the Spotify OAuth authorization URL.
func (p *Provider) GetAuthURL(state string) string {
	return p.oauth.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("show_dialog", "true"))
}

// ExchangeCode exchanges the authorization code for access and refresh tokens.
func (p *Provider) ExchangeCode(ctx context.Context, code string) (*providers.AuthResult, error) {
	token, err := p.oauth.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code: %w", err)
	}

	// Fetch user profile to get display name
	userInfo, err := p.fetchUserProfile(ctx, token.AccessToken)
	if err != nil {
		p.logger.Warn("failed to fetch user profile, continuing without display name", "error", err)
		userInfo = &spotifyUser{}
	}

	return &providers.AuthResult{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		Expiry:       token.Expiry,
		DisplayName:  userInfo.DisplayName,
		UserID:       userInfo.ID,
	}, nil
}

// RefreshToken refreshes an expired access token.
func (p *Provider) RefreshToken(ctx context.Context, refreshToken string) (*providers.AuthResult, error) {
	token := &oauth2.Token{
		RefreshToken: refreshToken,
	}

	tokenSource := p.oauth.TokenSource(ctx, token)
	newToken, err := tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}

	// Fetch user profile to update display name if needed
	userInfo, err := p.fetchUserProfile(ctx, newToken.AccessToken)
	if err != nil {
		p.logger.Warn("failed to fetch user profile during refresh", "error", err)
		userInfo = &spotifyUser{}
	}

	return &providers.AuthResult{
		AccessToken:  newToken.AccessToken,
		RefreshToken: newToken.RefreshToken,
		Expiry:       newToken.Expiry,
		DisplayName:  userInfo.DisplayName,
		UserID:       userInfo.ID,
	}, nil
}

// Disconnect performs cleanup when disconnecting from Spotify.
// Spotify doesn't support token revocation via API, so we just return nil.
func (p *Provider) Disconnect(ctx context.Context) error {
	p.logger.Info("disconnecting from Spotify", "user", p.user.Username)
	// Spotify doesn't have a token revocation endpoint
	// The tokens will be deleted from the database by the handler
	return nil
}

// Governing: SPEC music-provider-integration REQ-PROV-013 (transparent token refresh), REQ-PROV-032 (auto refresh before API calls)
// getValidToken returns a valid access token, refreshing (and persisting) if necessary.
func (p *Provider) getValidToken(ctx context.Context) (string, error) {
	if p.auth == nil {
		return "", fmt.Errorf("no spotify auth configured")
	}

	// Check if token is still valid (with 5 minute buffer)
	if time.Now().Add(5 * time.Minute).Before(p.auth.Expiry) {
		return p.auth.AccessToken, nil
	}

	// Token expired or about to expire, refresh it
	p.logger.Debug("refreshing expired Spotify token")
	return p.refreshAndPersist(ctx)
}

// Governing: SPEC music-provider-integration REQ-PROV-013 (refreshed tokens persisted to SpotifyAuth)
// refreshAndPersist refreshes the access token, updates the in-memory auth
// record, and persists the new token (and any rotated refresh token) to the
// database. Provider instances are per-sync-run and used sequentially, so no
// locking is done here; if a provider instance were ever shared across
// goroutines, concurrent refreshes could race on p.auth (last write wins,
// which is harmless for tokens but worth noting).
func (p *Provider) refreshAndPersist(ctx context.Context) (string, error) {
	result, err := p.RefreshToken(ctx, p.auth.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("failed to refresh token: %w", err)
	}

	p.auth.AccessToken = result.AccessToken
	p.auth.Expiry = result.Expiry
	if result.RefreshToken != "" {
		p.auth.RefreshToken = result.RefreshToken
	}

	p.persistAuth(ctx)

	return result.AccessToken, nil
}

// persistAuth writes the current in-memory tokens back to the SpotifyAuth row.
// Encryption is applied by the hooks registered on the ent client. Persistence
// failures are logged but not fatal: the in-memory token remains valid for the
// rest of this run.
func (p *Provider) persistAuth(ctx context.Context) {
	if p.db == nil || p.auth == nil || p.auth.ID == 0 {
		return
	}

	err := p.db.SpotifyAuth.UpdateOneID(p.auth.ID).
		SetAccessToken(p.auth.AccessToken).
		SetRefreshToken(p.auth.RefreshToken).
		SetExpiry(p.auth.Expiry).
		Exec(ctx)
	if err != nil {
		p.logger.Warn("failed to persist refreshed Spotify token", "error", err)
		return
	}
	p.logger.Debug("persisted refreshed Spotify token", "auth_id", p.auth.ID)
}

// Governing: SPEC error-handling REQ-ERR-002 Scenario 4 (single refresh+retry on mid-operation 401),
// SPEC error-handling REQ-ERR-002 (429 retriable), ADR-0020 (error handling and resilience)
// doAPIRequest performs an authenticated Spotify API request. If the API
// responds 401 (e.g. the token was revoked or expired server-side), it
// attempts exactly one token refresh and retries the request once before
// returning the response to the caller. 429 responses are retried after the
// server-provided Retry-After delay, up to httputil.MaxRateLimitRetries times.
func (p *Provider) doAPIRequest(ctx context.Context, method, reqURL string, body []byte) (*http.Response, error) {
	token, err := p.getValidToken(ctx)
	if err != nil {
		return nil, err
	}

	refreshedAfter401 := false

	for attempt := 0; ; attempt++ {
		resp, err := p.send(ctx, method, reqURL, body, token)
		if err != nil {
			return nil, err
		}

		switch {
		case resp.StatusCode == http.StatusTooManyRequests && attempt < httputil.MaxRateLimitRetries:
			retryAfter := httputil.RetryAfter(resp)
			p.closeBody(resp)

			p.logger.Warn("spotify API rate limited, retrying",
				"attempt", attempt+1,
				"retry_after", retryAfter)

			if err := httputil.Sleep(ctx, retryAfter); err != nil {
				return nil, err
			}

		case resp.StatusCode == http.StatusUnauthorized && !refreshedAfter401:
			p.closeBody(resp)

			p.logger.Debug("spotify API returned 401, refreshing token and retrying once")
			refreshedAfter401 = true
			token, err = p.refreshAndPersist(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to refresh token after 401: %w", err)
			}

		default:
			return resp, nil
		}
	}
}

// send performs a single HTTP request against the Spotify API.
func (p *Provider) send(ctx context.Context, method, reqURL string, body []byte, token string) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, reader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	// Governing: AGENTS.md "External API Etiquette" (descriptive User-Agent)
	req.Header.Set("User-Agent", httputil.UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return p.httpClient.Do(req)
}

// closeBody closes a response body, logging (not returning) any error.
func (p *Provider) closeBody(resp *http.Response) {
	if err := resp.Body.Close(); err != nil {
		p.logger.Warn("failed to close response body", "error", err)
	}
}

type spotifyUser struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

func (p *Provider) fetchUserProfile(ctx context.Context, accessToken string) (*spotifyUser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/v1/me", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	// Governing: AGENTS.md "External API Etiquette" (descriptive User-Agent)
	req.Header.Set("User-Agent", httputil.UserAgent)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer p.closeBody(resp)
	if resp.StatusCode != http.StatusOK {
		// Governing: ADR-0020, SPEC error-handling REQ-ERR-002/REQ-ERR-003
		return nil, resilience.NewHTTPStatusError(resp.StatusCode, fmt.Errorf("spotify API returned status %d", resp.StatusCode))
	}

	var user spotifyUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		// Governing: SPEC error-handling REQ-ERR-003 (unparseable response body is fatal)
		return nil, fmt.Errorf("failed to decode user profile response: %w: %w", providers.ErrMalformedResponse, err)
	}

	return &user, nil
}

type recentlyPlayedResponse struct {
	Items []struct {
		Track struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Duration int    `json:"duration_ms"`
			Album    struct {
				Name string `json:"name"`
			} `json:"album"`
			Artists []struct {
				Name string `json:"name"`
			} `json:"artists"`
			ExternalURLs struct {
				Spotify string `json:"spotify"`
			} `json:"external_urls"`
			// Governing: SPEC music-provider-integration REQ-PROV-022 (ISRC for cross-provider matching)
			ExternalIDs struct {
				ISRC string `json:"isrc"`
			} `json:"external_ids"`
		} `json:"track"`
		PlayedAt time.Time `json:"played_at"`
	} `json:"items"`
	Next    string `json:"next"`
	Cursors struct {
		After  string `json:"after"`
		Before string `json:"before"`
	} `json:"cursors"`
}

// Governing: SPEC music-provider-integration REQ-PROV-033 (paginated recently-played with since filter)
// GetRecentListens fetches recently played tracks from Spotify, paginating
// backwards in time with the "before" cursor until it reaches tracks older
// than `since`, runs out of pages, or hits maxRecentlyPlayedPages.
//
// PRACTICAL LIMITATION: Spotify's recently-played API only retains roughly the
// last 50 plays, so in practice the first page is usually the only page. The
// cursor loop below keeps us correct if Spotify ever widens that window. For
// full listening history, users must request a data export from Spotify's
// privacy settings at https://www.spotify.com/account/privacy/
//
// To capture the most history, sync should run frequently (at least every few
// hours) to catch new plays before they fall out of the retention window.
func (p *Provider) GetRecentListens(ctx context.Context, since time.Time, callback func([]providers.Track) error) error {
	p.logger.Info("fetching recent listens from spotify", "username", p.user.Username, "since", since)

	total := 0
	before := ""

	for page := 0; page < maxRecentlyPlayedPages; page++ {
		reqURL := p.baseURL + "/v1/me/player/recently-played?limit=50"
		if before != "" {
			reqURL += "&before=" + url.QueryEscape(before)
		}

		resp, err := p.doAPIRequest(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return err
		}

		if resp.StatusCode != http.StatusOK {
			p.closeBody(resp)
			// Governing: ADR-0020, SPEC error-handling REQ-ERR-002/REQ-ERR-003
			return resilience.NewHTTPStatusError(resp.StatusCode, fmt.Errorf("spotify API returned status %d", resp.StatusCode))
		}

		var result recentlyPlayedResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			p.closeBody(resp)
			// Governing: SPEC error-handling REQ-ERR-003 (unparseable response body is fatal)
			return fmt.Errorf("failed to decode recently-played response: %w: %w", providers.ErrMalformedResponse, err)
		}
		p.closeBody(resp)

		if len(result.Items) == 0 {
			break
		}

		// Keep only plays newer than `since`; anything at or before it (and
		// everything on later pages) has already been synced.
		tracks := make([]providers.Track, 0, len(result.Items))
		reachedSince := false
		for _, item := range result.Items {
			if !item.PlayedAt.After(since) {
				reachedSince = true
				continue
			}

			artist := ""
			if len(item.Track.Artists) > 0 {
				artist = item.Track.Artists[0].Name
			}

			tracks = append(tracks, providers.Track{
				ID:         item.Track.ID,
				Name:       item.Track.Name,
				Artist:     artist,
				Album:      item.Track.Album.Name,
				DurationMs: item.Track.Duration,
				PlayedAt:   item.PlayedAt,
				URL:        item.Track.ExternalURLs.Spotify,
				ISRC:       item.Track.ExternalIDs.ISRC,
			})
		}

		if len(tracks) > 0 {
			total += len(tracks)
			if err := callback(tracks); err != nil {
				return err
			}
		}

		if reachedSince || result.Cursors.Before == "" || result.Next == "" {
			break
		}
		before = result.Cursors.Before
	}

	p.logger.Info("fetched recent listens from spotify",
		"count", total,
		"note", "Spotify API retains roughly the last 50 plays - older history unavailable")

	return nil
}

type playlistsResponse struct {
	Items []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Images      []struct {
			URL    string `json:"url"`
			Height int    `json:"height"`
			Width  int    `json:"width"`
		} `json:"images"`
		ExternalURLs struct {
			Spotify string `json:"spotify"`
		} `json:"external_urls"`
		Tracks struct {
			Total int `json:"total"`
		} `json:"tracks"`
	} `json:"items"`
	Next  string `json:"next"`
	Total int    `json:"total"`
}

type playlistTracksResponse struct {
	Items []struct {
		Track struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			DurationMs int    `json:"duration_ms"`
			Artists    []struct {
				Name string `json:"name"`
			} `json:"artists"`
			Album struct {
				Name string `json:"name"`
			} `json:"album"`
			ExternalURLs struct {
				Spotify string `json:"spotify"`
			} `json:"external_urls"`
			// Governing: SPEC music-provider-integration REQ-PROV-022 (ISRC for cross-provider matching)
			ExternalIDs struct {
				ISRC string `json:"isrc"`
			} `json:"external_ids"`
		} `json:"track"`
	} `json:"items"`
	Next  string `json:"next"`
	Total int    `json:"total"`
}

func (p *Provider) GetPlaylists(ctx context.Context) ([]providers.Playlist, error) {
	p.logger.Info("fetching playlists from spotify", "username", p.user.Username)

	var allPlaylists []providers.Playlist
	nextURL := p.baseURL + "/v1/me/playlists?limit=50"

	// Paginate through all playlists
	for nextURL != "" {
		resp, err := p.doAPIRequest(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			p.closeBody(resp)
			// Governing: ADR-0020, SPEC error-handling REQ-ERR-002/REQ-ERR-003
			return nil, resilience.NewHTTPStatusError(resp.StatusCode, fmt.Errorf("spotify API returned status %d", resp.StatusCode))
		}

		var result playlistsResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			p.closeBody(resp)
			// Governing: SPEC error-handling REQ-ERR-003 (unparseable response body is fatal)
			return nil, fmt.Errorf("failed to decode playlists response: %w: %w", providers.ErrMalformedResponse, err)
		}
		p.closeBody(resp)

		for _, item := range result.Items {
			// Get the best image (first one is usually the largest)
			imageURL := ""
			if len(item.Images) > 0 {
				imageURL = item.Images[0].URL
			}

			// Fetch tracks, unique artists and albums for this playlist
			tracks, uniqueArtists, uniqueAlbums := p.getPlaylistTracks(ctx, item.ID)

			allPlaylists = append(allPlaylists, providers.Playlist{
				ID:            item.ID,
				Name:          item.Name,
				Description:   item.Description,
				ImageURL:      imageURL,
				ExternalURL:   item.ExternalURLs.Spotify,
				TrackCount:    item.Tracks.Total,
				UniqueArtists: uniqueArtists,
				UniqueAlbums:  uniqueAlbums,
				Tracks:        tracks,
			})
		}

		nextURL = result.Next
	}

	p.logger.Info("fetched playlists from spotify", "count", len(allPlaylists))
	return allPlaylists, nil
}

// getPlaylistTracks fetches all tracks in a playlist along with unique artist/album counts
func (p *Provider) getPlaylistTracks(ctx context.Context, playlistID string) (tracks []providers.Track, uniqueArtists, uniqueAlbums int) {
	artists := make(map[string]struct{})
	albums := make(map[string]struct{})

	// Governing: SPEC music-provider-integration REQ-PROV-022 (external_ids requested for ISRC)
	nextURL := fmt.Sprintf("%s/v1/playlists/%s/tracks?limit=100&fields=items(track(id,name,duration_ms,artists(name),album(name),external_urls,external_ids)),next,total", p.baseURL, playlistID)

	for nextURL != "" {
		resp, err := p.doAPIRequest(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			p.logger.Debug("failed to fetch playlist tracks", "error", err)
			break
		}

		if resp.StatusCode != http.StatusOK {
			p.closeBody(resp)
			p.logger.Debug("spotify API returned non-OK status for playlist tracks", "status", resp.StatusCode)
			break
		}

		var result playlistTracksResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			p.closeBody(resp)
			p.logger.Debug("failed to decode playlist tracks response", "error", err)
			break
		}
		p.closeBody(resp)

		for _, item := range result.Items {
			if item.Track.ID == "" {
				continue // Skip local files or unavailable tracks
			}

			// Get artist name (use first artist)
			artistName := ""
			if len(item.Track.Artists) > 0 {
				artistName = item.Track.Artists[0].Name
			}

			// Track unique artists and albums
			for _, artist := range item.Track.Artists {
				if artist.Name != "" {
					artists[artist.Name] = struct{}{}
				}
			}
			if item.Track.Album.Name != "" {
				albums[item.Track.Album.Name] = struct{}{}
			}

			// Add track to list
			tracks = append(tracks, providers.Track{
				ID:         item.Track.ID,
				Name:       item.Track.Name,
				Artist:     artistName,
				Album:      item.Track.Album.Name,
				DurationMs: item.Track.DurationMs,
				URL:        item.Track.ExternalURLs.Spotify,
				ISRC:       item.Track.ExternalIDs.ISRC,
			})
		}

		nextURL = result.Next
	}

	return tracks, len(artists), len(albums)
}

// Governing: SPEC music-provider-integration REQ-PROV-003, REQ-PROV-030 (Spotify playlist creation)
// CreatePlaylist creates a private playlist on Spotify and adds the given tracks.
func (p *Provider) CreatePlaylist(ctx context.Context, name, description string, tracks []providers.Track) error {
	_, err := p.createPlaylist(ctx, name, description, tracks)
	return err
}

// createPlaylist creates the playlist and returns the new Spotify playlist ID.
// The PlaylistManager interface does not surface the created ID (changing it
// would ripple through every provider), so the exported method discards it;
// it is returned here for tests and future callers.
func (p *Provider) createPlaylist(ctx context.Context, name, description string, tracks []providers.Track) (string, error) {
	p.logger.Info("creating playlist on spotify", "username", p.user.Username, "name", name, "track_count", len(tracks))

	if len(tracks) == 0 {
		return "", fmt.Errorf("cannot create empty playlist")
	}

	accessToken, err := p.getValidToken(ctx)
	if err != nil {
		return "", err
	}

	// Get user ID first
	userInfo, err := p.fetchUserProfile(ctx, accessToken)
	if err != nil {
		return "", fmt.Errorf("failed to get user ID: %w", err)
	}
	if userInfo.ID == "" {
		return "", fmt.Errorf("spotify user profile has no ID")
	}

	// Create the playlist (private by default)
	payload, err := json.Marshal(map[string]any{
		"name":        name,
		"description": description,
		"public":      false,
	})
	if err != nil {
		return "", err
	}

	createURL := fmt.Sprintf("%s/v1/users/%s/playlists", p.baseURL, url.PathEscape(userInfo.ID))
	resp, err := p.doAPIRequest(ctx, http.MethodPost, createURL, payload)
	if err != nil {
		return "", fmt.Errorf("failed to create playlist: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		p.closeBody(resp)
		// Governing: ADR-0020, SPEC error-handling REQ-ERR-002/REQ-ERR-003
		return "", resilience.NewHTTPStatusError(resp.StatusCode, fmt.Errorf("spotify API returned status %d", resp.StatusCode))
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		p.closeBody(resp)
		// Governing: SPEC error-handling REQ-ERR-003 (unparseable response body is fatal)
		return "", fmt.Errorf("failed to decode create playlist response: %w: %w", providers.ErrMalformedResponse, err)
	}
	p.closeBody(resp)

	if created.ID == "" {
		return "", fmt.Errorf("spotify did not return a playlist ID")
	}

	// Add tracks in batches of at most 100 URIs (Spotify's per-request limit).
	uris := make([]string, 0, len(tracks))
	for _, track := range tracks {
		if track.ID == "" {
			continue // Skip local files or unavailable tracks
		}
		uris = append(uris, "spotify:track:"+track.ID)
	}

	addURL := fmt.Sprintf("%s/v1/playlists/%s/tracks", p.baseURL, url.PathEscape(created.ID))
	for start := 0; start < len(uris); start += addTracksBatchSize {
		end := start + addTracksBatchSize
		if end > len(uris) {
			end = len(uris)
		}

		batch, err := json.Marshal(map[string]any{"uris": uris[start:end]})
		if err != nil {
			return "", err
		}

		resp, err := p.doAPIRequest(ctx, http.MethodPost, addURL, batch)
		if err != nil {
			return "", fmt.Errorf("failed to add tracks to playlist: %w", err)
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			p.closeBody(resp)
			// Governing: ADR-0020, SPEC error-handling REQ-ERR-002/REQ-ERR-003
			return "", resilience.NewHTTPStatusError(resp.StatusCode, fmt.Errorf("spotify API returned status %d", resp.StatusCode))
		}
		p.closeBody(resp)
	}

	p.logger.Info("created playlist on spotify",
		"playlist_id", created.ID,
		"name", name,
		"track_count", len(uris))

	return created.ID, nil
}
