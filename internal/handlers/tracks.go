package handlers

import (
	"context"
	"net/http"
	"strconv"

	"spotter/ent"
	"spotter/ent/artist"
	"spotter/ent/listen"
	"spotter/ent/track"
	"spotter/ent/user"
	"spotter/internal/views/components"
	"spotter/internal/views/tracks"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) TrackShow(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	trackID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "Invalid track ID", http.StatusBadRequest)
		return
	}

	// Get the track with artist and album
	t, err := h.Client.Track.Query().
		Where(track.ID(trackID)).
		WithArtist(func(q *ent.ArtistQuery) {
			q.WithImages()
			q.Where(artist.HasUserWith(user.ID(u.ID)))
		}).
		WithAlbum(func(q *ent.AlbumQuery) {
			q.WithImages()
		}).
		Only(r.Context())
	if err != nil {
		h.Logger.Error("failed to get track", "error", err, "id", trackID)
		http.Error(w, "Track not found", http.StatusNotFound)
		return
	}

	// Verify track belongs to user's artist
	if t.Edges.Artist == nil {
		http.Error(w, "Track not found", http.StatusNotFound)
		return
	}

	// Get timeframe from query
	timeframe := r.URL.Query().Get("timeframe")
	if timeframe == "" {
		timeframe = "30d"
	}

	// Get stats
	stats := h.getTrackStats(r.Context(), u.ID, t, timeframe)

	h.Render(w, r, tracks.Show(t, stats, h.Config, timeframe))
}

func (h *Handler) TrackChart(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	trackID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "Invalid track ID", http.StatusBadRequest)
		return
	}

	// Get track with artist
	t, err := h.Client.Track.Query().
		Where(track.ID(trackID)).
		WithArtist(func(q *ent.ArtistQuery) {
			q.Where(artist.HasUserWith(user.ID(u.ID)))
		}).
		WithAlbum().
		Only(r.Context())
	if err != nil {
		http.Error(w, "Track not found", http.StatusNotFound)
		return
	}

	if t.Edges.Artist == nil {
		http.Error(w, "Track not found", http.StatusNotFound)
		return
	}

	timeframe := r.URL.Query().Get("timeframe")
	if timeframe == "" {
		timeframe = "30d"
	}

	artistName := t.Edges.Artist.Name
	albumName := ""
	if t.Edges.Album != nil {
		albumName = t.Edges.Album.Name
	}

	data := h.getProviderHistory(r.Context(), u.ID, artistName, albumName, t.Name, timeframe)
	h.Render(w, r, tracks.ProviderHistoryChartContent(trackID, data))
}

func (h *Handler) getTrackStats(ctx context.Context, userID int, t *ent.Track, timeframe string) *tracks.TrackStats {
	stats := &tracks.TrackStats{
		ListensByHour:      make([]components.ChartDataPoint, 24),
		ListensByDayOfWeek: make([]components.ChartDataPoint, 7),
		ListensByMonth:     make([]components.ChartDataPoint, 12),
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

	// Build query for this track's listens
	query := h.Client.Listen.Query().
		Where(
			listen.HasUserWith(user.ID(userID)),
			listen.TrackName(t.Name),
		)

	// Add artist filter if available
	if t.Edges.Artist != nil {
		query = query.Where(listen.ArtistName(t.Edges.Artist.Name))
	}

	// Add album filter if available
	if t.Edges.Album != nil {
		query = query.Where(listen.AlbumName(t.Edges.Album.Name))
	}

	listens, err := query.Order(ent.Asc(listen.FieldPlayedAt)).All(ctx)
	if err != nil {
		h.Logger.Error("failed to get track listens", "error", err)
		return stats
	}

	stats.TotalListens = len(listens)

	if len(listens) > 0 {
		stats.FirstListen = listens[0].PlayedAt
		stats.LastListen = listens[len(listens)-1].PlayedAt
	}

	// Count provider stats and time distributions
	providerCounts := make(map[string]int)

	for _, l := range listens {
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

	// Provider breakdown
	for provider, count := range providerCounts {
		stats.ListensByProvider = append(stats.ListensByProvider, components.ChartDataPoint{
			Label: provider,
			Value: float64(count),
		})
	}

	// Get provider history
	artistName := ""
	if t.Edges.Artist != nil {
		artistName = t.Edges.Artist.Name
	}
	albumName := ""
	if t.Edges.Album != nil {
		albumName = t.Edges.Album.Name
	}
	stats.ProviderHistory = h.getProviderHistory(ctx, userID, artistName, albumName, t.Name, timeframe)

	return stats
}

// TrackIndex shows all tracks for the user
func (h *Handler) TrackIndex(w http.ResponseWriter, r *http.Request) {
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

	// Query tracks with pagination
	trackList, err := h.Client.Track.Query().
		Where(track.HasArtistWith(artist.HasUserWith(user.ID(u.ID)))).
		WithArtist().
		WithAlbum(func(q *ent.AlbumQuery) {
			q.WithImages()
		}).
		Order(ent.Desc(track.FieldUpdatedAt)).
		Limit(pageSize).
		Offset(offset).
		All(r.Context())
	if err != nil {
		h.Logger.Error("failed to query tracks", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Get total count for pagination
	total, err := h.Client.Track.Query().
		Where(track.HasArtistWith(artist.HasUserWith(user.ID(u.ID)))).
		Count(r.Context())
	if err != nil {
		h.Logger.Error("failed to count tracks", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	totalPages := (total + pageSize - 1) / pageSize

	h.Render(w, r, tracks.Index(trackList, page, totalPages, h.Config))
}
