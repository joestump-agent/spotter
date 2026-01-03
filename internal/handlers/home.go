package handlers

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"time"

	"spotter/ent"
	"spotter/ent/listen"
	"spotter/ent/user"
	"spotter/internal/views/home"
)

func (h *Handler) Home(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	stats, err := h.getHomeStats(r.Context(), u)
	if err != nil {
		h.Logger.Error("failed to get home stats", "error", err)
		// Continue with empty stats rather than failing
		stats = &home.HomeStats{
			NavidromeURL: h.Config.Navidrome.BaseURL,
			LoggedInUser: u.Username,
		}
	}

	h.Render(w, r, home.Index(h.Config, stats))
}

func (h *Handler) getHomeStats(ctx context.Context, u *ent.User) (*home.HomeStats, error) {
	// Refresh user to get all auth edges
	u, err := h.Client.User.Query().
		Where(user.ID(u.ID)).
		WithSpotifyAuth().
		WithNavidromeAuth().
		WithLastfmAuth().
		Only(ctx)
	if err != nil {
		return nil, err
	}

	stats := &home.HomeStats{
		NavidromeURL: h.Config.Navidrome.BaseURL,
		LoggedInUser: u.Username,
		Providers:    make([]home.ProviderStats, 0, 3),
	}

	// Get overall stats
	stats.TotalListens, _ = h.Client.Listen.Query().
		Where(listen.HasUserWith(user.ID(u.ID))).
		Count(ctx)

	// Get unique artists
	var artistResults []struct {
		ArtistName string `json:"artist_name"`
	}
	err = h.Client.Listen.Query().
		Where(listen.HasUserWith(user.ID(u.ID))).
		GroupBy(listen.FieldArtistName).
		Scan(ctx, &artistResults)
	if err == nil {
		stats.UniqueArtists = len(artistResults)
	}

	// Get unique albums
	var albumResults []struct {
		AlbumName string `json:"album_name"`
	}
	err = h.Client.Listen.Query().
		Where(listen.HasUserWith(user.ID(u.ID))).
		GroupBy(listen.FieldAlbumName).
		Scan(ctx, &albumResults)
	if err == nil {
		stats.UniqueAlbums = len(albumResults)
	}

	// Navidrome provider stats
	navidromeStats := home.ProviderStats{
		Name:      "Navidrome",
		Connected: u.Edges.NavidromeAuth != nil,
	}
	if u.Edges.NavidromeAuth != nil {
		navidromeStats.Username = u.Username
		navidromeStats.LastSyncedAt = u.Edges.NavidromeAuth.LastSyncedAt
		navidromeStats.ServerURL = h.Config.Navidrome.BaseURL
		navidromeStats.ServerOnline = h.checkNavidromeOnline(u.Username, u.Edges.NavidromeAuth.Password)
		navidromeStats.TotalListens, _ = h.Client.Listen.Query().
			Where(
				listen.HasUserWith(user.ID(u.ID)),
				listen.Source("navidrome"),
			).Count(ctx)
		navidromeStats.UniqueArtists = h.countUniqueArtists(ctx, u.ID, "navidrome")
		navidromeStats.UniqueAlbums = h.countUniqueAlbums(ctx, u.ID, "navidrome")
	}
	stats.Providers = append(stats.Providers, navidromeStats)

	// Spotify provider stats
	spotifyStats := home.ProviderStats{
		Name:      "Spotify",
		Connected: u.Edges.SpotifyAuth != nil,
	}
	if u.Edges.SpotifyAuth != nil {
		spotifyStats.Username = u.Edges.SpotifyAuth.DisplayName
		if spotifyStats.Username == "" {
			spotifyStats.Username = "Connected"
		}
		spotifyStats.LastSyncedAt = u.Edges.SpotifyAuth.LastSyncedAt
		spotifyStats.TotalListens, _ = h.Client.Listen.Query().
			Where(
				listen.HasUserWith(user.ID(u.ID)),
				listen.Source("spotify"),
			).Count(ctx)
		spotifyStats.UniqueArtists = h.countUniqueArtists(ctx, u.ID, "spotify")
		spotifyStats.UniqueAlbums = h.countUniqueAlbums(ctx, u.ID, "spotify")
	}
	stats.Providers = append(stats.Providers, spotifyStats)

	// Last.fm provider stats
	lastfmStats := home.ProviderStats{
		Name:      "Last.fm",
		Connected: u.Edges.LastfmAuth != nil,
	}
	if u.Edges.LastfmAuth != nil {
		lastfmStats.Username = u.Edges.LastfmAuth.Username
		lastfmStats.LastSyncedAt = u.Edges.LastfmAuth.LastSyncedAt
		lastfmStats.TotalListens, _ = h.Client.Listen.Query().
			Where(
				listen.HasUserWith(user.ID(u.ID)),
				listen.Source("lastfm"),
			).Count(ctx)
		lastfmStats.UniqueArtists = h.countUniqueArtists(ctx, u.ID, "lastfm")
		lastfmStats.UniqueAlbums = h.countUniqueAlbums(ctx, u.ID, "lastfm")
	}
	stats.Providers = append(stats.Providers, lastfmStats)

	return stats, nil
}

func (h *Handler) countUniqueArtists(ctx context.Context, userID int, source string) int {
	var results []struct {
		ArtistName string `json:"artist_name"`
	}
	err := h.Client.Listen.Query().
		Where(
			listen.HasUserWith(user.ID(userID)),
			listen.Source(source),
		).
		GroupBy(listen.FieldArtistName).
		Scan(ctx, &results)
	if err != nil {
		return 0
	}
	return len(results)
}

func (h *Handler) countUniqueAlbums(ctx context.Context, userID int, source string) int {
	var results []struct {
		AlbumName string `json:"album_name"`
	}
	err := h.Client.Listen.Query().
		Where(
			listen.HasUserWith(user.ID(userID)),
			listen.Source(source),
		).
		GroupBy(listen.FieldAlbumName).
		Scan(ctx, &results)
	if err != nil {
		return 0
	}
	return len(results)
}

// checkNavidromeOnline pings the Navidrome server to check if it's reachable
func (h *Handler) checkNavidromeOnline(username, password string) bool {
	if h.Config.Navidrome.BaseURL == "" {
		return false
	}

	// Use a short timeout for the ping
	client := &http.Client{Timeout: 5 * time.Second}

	// Build ping URL with auth
	salt := "spotter"
	hash := md5.New()
	hash.Write([]byte(password + salt))
	token := hex.EncodeToString(hash.Sum(nil))

	url := h.Config.Navidrome.BaseURL + "/rest/ping.view?u=" + username + "&t=" + token + "&s=" + salt + "&v=1.16.1&c=spotter&f=json"

	resp, err := client.Get(url)
	if err != nil {
		h.Logger.Debug("navidrome ping failed", "error", err)
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

func (h *Handler) GeneratePlaylist(w http.ResponseWriter, r *http.Request) {
	prompt := r.FormValue("prompt")

	// TODO: Integrate with AI service to generate playlist
	h.Logger.Info("Generating playlist", "prompt", prompt)

	// Simulate work
	time.Sleep(2 * time.Second)

	w.Write([]byte("<div class=\"alert alert-success\" role=\"alert\"><span class=\"icon-[heroicons--check-circle] w-5 h-5\"></span><span>Playlist generation started based on prompt: \"" + prompt + "\". Check Navidrome shortly.</span></div>"))
}
