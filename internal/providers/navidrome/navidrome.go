package navidrome

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"spotter/ent"
	"spotter/internal/config"
	"spotter/internal/providers"
)

type Provider struct {
	logger *slog.Logger
	config *config.Config
	user   *ent.User
	auth   *ent.NavidromeAuth
}

// Ensure Provider implements interfaces
var _ providers.HistoryFetcher = (*Provider)(nil)
var _ providers.PlaylistManager = (*Provider)(nil)

// New returns a factory that creates Navidrome providers for a given user.
func New(logger *slog.Logger, cfg *config.Config) providers.Factory {
	return func(ctx context.Context, user *ent.User) (providers.Provider, error) {
		// Check if the user has Navidrome authentication data.
		// We expect the caller to have loaded the edges (e.g. WithNavidromeAuth()).
		if user.Edges.NavidromeAuth == nil {
			return nil, nil
		}

		return &Provider{
			logger: logger,
			config: cfg,
			user:   user,
			auth:   user.Edges.NavidromeAuth,
		}, nil
	}
}

func (p *Provider) Type() providers.Type {
	return providers.TypeNavidrome
}

func (p *Provider) GetRecentListens(ctx context.Context, since time.Time) ([]providers.Track, error) {
	p.logger.Info("fetching recent listens from navidrome", "username", p.user.Username, "since", since)

	// Generate Auth Parameters
	salt := generateSalt()
	token := generateToken(p.auth.Password, salt)

	params := url.Values{}
	params.Set("u", p.user.Username)
	params.Set("t", token)
	params.Set("s", salt)
	params.Set("v", "1.16.1") // Target Subsonic API version
	params.Set("c", "spotter")
	params.Set("f", "json")

	// Construct URL
	// Note: getNowPlaying returns what is currently playing or recently played by users.
	// We will filter by the current user's username.
	baseURL := strings.TrimSuffix(p.config.Navidrome.BaseURL, "/")
	apiURL := fmt.Sprintf("%s/rest/getNowPlaying.view?%s", baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("navidrome API returned status: %d", resp.StatusCode)
	}

	var result struct {
		SubsonicResponse struct {
			Status string `json:"status"`
			Error  struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			NowPlaying struct {
				Entry []struct {
					ID         string `json:"id"`
					Title      string `json:"title"`
					Artist     string `json:"artist"`
					Album      string `json:"album"`
					Duration   int    `json:"duration"`   // Seconds
					MinutesAgo int    `json:"minutesAgo"` // Minutes since played
					Username   string `json:"username"`
				} `json:"entry"`
			} `json:"nowPlaying"`
		} `json:"subsonic-response"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if result.SubsonicResponse.Status == "failed" {
		return nil, fmt.Errorf("navidrome API error: %s", result.SubsonicResponse.Error.Message)
	}

	var tracks []providers.Track
	for _, entry := range result.SubsonicResponse.NowPlaying.Entry {
		// Filter by username to ensure we only get this user's listens
		if entry.Username != p.user.Username {
			continue
		}

		// Calculate PlayedAt
		// minutesAgo is relative to now
		playedAt := time.Now().Add(-time.Duration(entry.MinutesAgo) * time.Minute)

		// Filter by 'since'
		if playedAt.Before(since) {
			continue
		}

		tracks = append(tracks, providers.Track{
			ID:         entry.ID,
			Name:       entry.Title,
			Artist:     entry.Artist,
			Album:      entry.Album,
			DurationMs: entry.Duration * 1000,
			PlayedAt:   playedAt,
			// Constructing a web player link.
			// Navidrome web UI typical route: /app/#/song/{id}
			URL: fmt.Sprintf("%s/app/#/song/%s", baseURL, entry.ID),
		})
	}

	return tracks, nil
}

func (p *Provider) GetPlaylists(ctx context.Context) ([]providers.Playlist, error) {
	p.logger.Info("fetching playlists from navidrome", "username", p.user.Username)
	return []providers.Playlist{}, nil
}

func (p *Provider) CreatePlaylist(ctx context.Context, name, description string, tracks []providers.Track) error {
	p.logger.Info("creating playlist on navidrome", "username", p.user.Username, "name", name, "track_count", len(tracks))

	if len(tracks) == 0 {
		return fmt.Errorf("cannot create empty playlist")
	}

	// TODO: Implement actual Navidrome API call
	// Subsonic API: createPlaylist

	return nil
}

func generateSalt() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		// Fallback if random fails, though unlikely
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func generateToken(password, salt string) string {
	hash := md5.New()
	hash.Write([]byte(password + salt))
	return hex.EncodeToString(hash.Sum(nil))
}
