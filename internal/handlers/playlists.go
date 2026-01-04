package handlers

import (
	"context"
	"net/http"
	"sort"
	"strconv"

	"spotter/ent"
	"spotter/ent/artist"
	"spotter/ent/listen"
	"spotter/ent/playlist"
	"spotter/ent/track"
	"spotter/ent/user"
	"spotter/internal/views/components"
	"spotter/internal/views/playlists"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) Playlists(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	// Refresh user to get pagination settings
	u, err := h.Client.User.Query().
		Where(user.ID(u.ID)).
		Only(r.Context())
	if err != nil {
		h.Logger.Error("failed to query user", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Get page number from query
	page := 1
	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
	}

	pageSize := u.PaginationSize
	offset := (page - 1) * pageSize

	// Query playlists with pagination
	pls, err := h.Client.Playlist.Query().
		Where(playlist.HasUserWith(user.ID(u.ID))).
		Order(ent.Desc(playlist.FieldUpdatedAt)).
		Limit(pageSize).
		Offset(offset).
		All(r.Context())
	if err != nil {
		h.Logger.Error("failed to query playlists", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Get total count for pagination
	total, err := h.Client.Playlist.Query().
		Where(playlist.HasUserWith(user.ID(u.ID))).
		Count(r.Context())
	if err != nil {
		h.Logger.Error("failed to count playlists", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	totalPages := (total + pageSize - 1) / pageSize

	h.Render(w, r, playlists.Index(pls, page, totalPages, h.Config))
}

func (h *Handler) PlaylistShow(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	playlistID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "Invalid playlist ID", http.StatusBadRequest)
		return
	}

	// Get the playlist
	pl, err := h.Client.Playlist.Query().
		Where(
			playlist.ID(playlistID),
			playlist.HasUserWith(user.ID(u.ID)),
		).
		Only(r.Context())
	if err != nil {
		h.Logger.Error("failed to get playlist", "error", err, "id", playlistID)
		http.Error(w, "Playlist not found", http.StatusNotFound)
		return
	}

	// Get timeframe from query
	timeframe := r.URL.Query().Get("timeframe")
	if timeframe == "" {
		timeframe = "30d"
	}

	// Get tracks for this playlist (from listens data)
	tracks := h.getPlaylistTracks(r.Context(), u.ID, pl)

	// Get stats
	stats := h.getPlaylistStats(r.Context(), u.ID, pl, tracks, timeframe)

	h.Render(w, r, playlists.Show(pl, tracks, stats, h.Config, timeframe))
}

func (h *Handler) PlaylistChart(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	playlistID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "Invalid playlist ID", http.StatusBadRequest)
		return
	}

	// Get playlist
	pl, err := h.Client.Playlist.Query().
		Where(
			playlist.ID(playlistID),
			playlist.HasUserWith(user.ID(u.ID)),
		).
		Only(r.Context())
	if err != nil {
		http.Error(w, "Playlist not found", http.StatusNotFound)
		return
	}

	timeframe := r.URL.Query().Get("timeframe")
	if timeframe == "" {
		timeframe = "30d"
	}

	// Get tracks for this playlist to build the provider history
	tracks := h.getPlaylistTracks(r.Context(), u.ID, pl)
	data := h.getPlaylistProviderHistory(r.Context(), u.ID, tracks, timeframe)

	h.Render(w, r, playlists.ProviderHistoryChartContent(playlistID, data))
}

func (h *Handler) getPlaylistTracks(ctx context.Context, userID int, pl *ent.Playlist) []playlists.PlaylistTrack {
	// Since we don't have direct playlist-track associations,
	// we'll get unique tracks from listens for this source
	// In a more complete implementation, you'd have a playlist_tracks table

	type trackInfo struct {
		TrackName  string `json:"track_name"`
		ArtistName string `json:"artist_name"`
		AlbumName  string `json:"album_name"`
	}

	var trackInfos []trackInfo
	err := h.Client.Listen.Query().
		Where(
			listen.HasUserWith(user.ID(userID)),
			listen.Source(pl.Source),
		).
		GroupBy(listen.FieldTrackName, listen.FieldArtistName, listen.FieldAlbumName).
		Scan(ctx, &trackInfos)
	if err != nil {
		h.Logger.Error("failed to get playlist tracks", "error", err)
		return nil
	}

	// Get listen counts and try to find matching catalog entries
	result := make([]playlists.PlaylistTrack, 0, len(trackInfos))

	for _, ti := range trackInfos {
		count, _ := h.Client.Listen.Query().
			Where(
				listen.HasUserWith(user.ID(userID)),
				listen.Source(pl.Source),
				listen.TrackName(ti.TrackName),
				listen.ArtistName(ti.ArtistName),
			).
			Count(ctx)

		pt := playlists.PlaylistTrack{
			Name:        ti.TrackName,
			ArtistName:  ti.ArtistName,
			AlbumName:   ti.AlbumName,
			ListenCount: count,
		}

		// Try to find matching track in catalog
		t, err := h.Client.Track.Query().
			Where(
				track.Name(ti.TrackName),
				track.HasArtistWith(artist.Name(ti.ArtistName)),
			).
			WithArtist().
			WithAlbum().
			First(ctx)
		if err == nil {
			pt.TrackID = t.ID
			if t.Edges.Artist != nil {
				pt.ArtistID = t.Edges.Artist.ID
			}
			if t.Edges.Album != nil {
				pt.AlbumID = t.Edges.Album.ID
			}
		}

		result = append(result, pt)
	}

	// Sort by listen count descending
	sort.Slice(result, func(i, j int) bool {
		return result[i].ListenCount > result[j].ListenCount
	})

	return result
}

func (h *Handler) getPlaylistStats(ctx context.Context, userID int, pl *ent.Playlist, tracks []playlists.PlaylistTrack, timeframe string) *playlists.PlaylistStats {
	stats := &playlists.PlaylistStats{
		ListensByHour:      make([]components.ChartDataPoint, 24),
		ListensByDayOfWeek: make([]components.ChartDataPoint, 7),
		ListensByMonth:     make([]components.ChartDataPoint, 12),
		TopArtists:         []components.ChartDataPoint{},
		TopTracks:          []components.ChartDataPoint{},
		GenreBreakdown:     []components.ChartDataPoint{},
	}

	// Initialize hour labels
	for i := 0; i < 24; i++ {
		stats.ListensByHour[i] = components.ChartDataPoint{Label: strconv.Itoa(i), Value: 0}
	}

	// Initialize day labels
	days := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	for i, day := range days {
		stats.ListensByDayOfWeek[i] = components.ChartDataPoint{Label: day, Value: 0}
	}

	// Initialize month labels
	months := []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	for i, month := range months {
		stats.ListensByMonth[i] = components.ChartDataPoint{Label: month, Value: 0}
	}

	// Get all listens for this source
	listens, err := h.Client.Listen.Query().
		Where(
			listen.HasUserWith(user.ID(userID)),
			listen.Source(pl.Source),
		).
		Order(ent.Asc(listen.FieldPlayedAt)).
		All(ctx)
	if err != nil {
		h.Logger.Error("failed to get playlist listens", "error", err)
		return stats
	}

	stats.TotalListens = len(listens)

	if len(listens) > 0 {
		stats.FirstListen = listens[0].PlayedAt
		stats.LastListen = listens[len(listens)-1].PlayedAt
	}

	// Count unique tracks, artists, and gather stats
	trackSet := make(map[string]bool)
	artistCounts := make(map[string]int)
	trackCounts := make(map[string]int)
	providerCounts := make(map[string]int)

	for _, l := range listens {
		trackKey := l.TrackName + "||" + l.ArtistName
		trackSet[trackKey] = true
		artistCounts[l.ArtistName]++
		trackCounts[l.TrackName]++
		providerCounts[l.Source]++

		// Hour stats
		hour := l.PlayedAt.Local().Hour()
		stats.ListensByHour[hour].Value++

		// Day of week stats
		day := int(l.PlayedAt.Local().Weekday())
		stats.ListensByDayOfWeek[day].Value++

		// Month stats
		month := int(l.PlayedAt.Local().Month()) - 1
		stats.ListensByMonth[month].Value++
	}

	stats.UniqueTracks = len(trackSet)
	stats.UniqueArtists = len(artistCounts)

	// Provider breakdown
	for provider, count := range providerCounts {
		stats.ListensByProvider = append(stats.ListensByProvider, components.ChartDataPoint{
			Label: provider,
			Value: float64(count),
		})
	}

	// Top artists (limit to 10)
	topArtists := make([]components.ChartDataPoint, 0, len(artistCounts))
	for artistName, count := range artistCounts {
		topArtists = append(topArtists, components.ChartDataPoint{
			Label: artistName,
			Value: float64(count),
		})
	}
	sort.Slice(topArtists, func(i, j int) bool {
		return topArtists[i].Value > topArtists[j].Value
	})
	if len(topArtists) > 10 {
		topArtists = topArtists[:10]
	}
	stats.TopArtists = topArtists

	// Top tracks (limit to 10)
	topTracks := make([]components.ChartDataPoint, 0, len(trackCounts))
	for trackName, count := range trackCounts {
		topTracks = append(topTracks, components.ChartDataPoint{
			Label: trackName,
			Value: float64(count),
		})
	}
	sort.Slice(topTracks, func(i, j int) bool {
		return topTracks[i].Value > topTracks[j].Value
	})
	if len(topTracks) > 10 {
		topTracks = topTracks[:10]
	}
	stats.TopTracks = topTracks

	// Genre breakdown - get from catalog if available
	genreCounts := make(map[string]int)
	for _, t := range tracks {
		if t.TrackID > 0 {
			tr, err := h.Client.Track.Query().
				Where(track.ID(t.TrackID)).
				Only(ctx)
			if err == nil && len(tr.Genres) > 0 {
				for _, g := range tr.Genres {
					genreCounts[g] += t.ListenCount
				}
			}
		}
	}

	genreBreakdown := make([]components.ChartDataPoint, 0, len(genreCounts))
	for genre, count := range genreCounts {
		genreBreakdown = append(genreBreakdown, components.ChartDataPoint{
			Label: genre,
			Value: float64(count),
		})
	}
	sort.Slice(genreBreakdown, func(i, j int) bool {
		return genreBreakdown[i].Value > genreBreakdown[j].Value
	})
	if len(genreBreakdown) > 10 {
		genreBreakdown = genreBreakdown[:10]
	}
	stats.GenreBreakdown = genreBreakdown

	// Get provider history
	stats.ProviderHistory = h.getPlaylistProviderHistory(ctx, userID, tracks, timeframe)

	return stats
}

func (h *Handler) getPlaylistProviderHistory(ctx context.Context, userID int, tracks []playlists.PlaylistTrack, timeframe string) components.StackedChartData {
	// For playlists, we'll use the general provider history function
	// Since playlist tracks span multiple artists/albums, we won't filter
	return h.getProviderHistory(ctx, userID, "", "", "", timeframe)
}
