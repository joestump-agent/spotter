package handlers

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"sort"
	"time"

	"spotter/ent"
	"spotter/ent/album"
	"spotter/ent/artist"
	"spotter/ent/listen"
	"spotter/ent/track"
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

	// Get all listens for this user
	listens, err := h.Client.Listen.Query().
		Where(listen.HasUserWith(user.ID(u.ID))).
		All(ctx)
	if err != nil {
		h.Logger.Error("failed to get listens", "error", err)
		listens = []*ent.Listen{}
	}

	stats.TotalListens = len(listens)

	// Process listens for various stats
	artistSet := make(map[string]bool)
	albumSet := make(map[string]bool)
	trackSet := make(map[string]bool)
	artistCounts := make(map[string]int)
	tagCounts := make(map[string]int)

	for _, l := range listens {
		artistSet[l.ArtistName] = true
		albumSet[l.AlbumName] = true
		trackSet[l.TrackName+"||"+l.ArtistName] = true
		artistCounts[l.ArtistName]++
	}

	stats.UniqueArtists = len(artistSet)
	stats.UniqueAlbums = len(albumSet)
	stats.UniqueTracks = len(trackSet)

	// Get enriched catalog counts
	stats.EnrichedArtistCount, _ = h.Client.Artist.Query().
		Where(artist.HasUserWith(user.ID(u.ID))).
		Count(ctx)

	stats.EnrichedAlbumCount, _ = h.Client.Album.Query().
		Where(album.HasUserWith(user.ID(u.ID))).
		Count(ctx)

	stats.EnrichedTrackCount, _ = h.Client.Track.Query().
		Where(track.HasArtistWith(artist.HasUserWith(user.ID(u.ID)))).
		Count(ctx)

	// Get tags from enriched tracks
	tracks, err := h.Client.Track.Query().
		Where(track.HasArtistWith(artist.HasUserWith(user.ID(u.ID)))).
		All(ctx)
	if err == nil {
		for _, t := range tracks {
			for _, tag := range t.Tags {
				tagCounts[tag]++
			}
		}
	}

	// Get tags from artists too
	artists, err := h.Client.Artist.Query().
		Where(artist.HasUserWith(user.ID(u.ID))).
		All(ctx)
	if err == nil {
		for _, a := range artists {
			listenCount := artistCounts[a.Name]
			for _, tag := range a.Tags {
				tagCounts[tag] += listenCount
			}
		}
	}

	// Build tag cloud (top 30)
	tagList := make([]struct {
		Tag   string
		Count int
	}, 0, len(tagCounts))
	for tag, count := range tagCounts {
		tagList = append(tagList, struct {
			Tag   string
			Count int
		}{tag, count})
	}
	sort.Slice(tagList, func(i, j int) bool {
		return tagList[i].Count > tagList[j].Count
	})
	stats.TagCloud = make([]string, 0, 30)
	for i, t := range tagList {
		if i >= 30 {
			break
		}
		stats.TagCloud = append(stats.TagCloud, t.Tag)
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
