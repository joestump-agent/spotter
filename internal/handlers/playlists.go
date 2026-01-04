package handlers

import (
	"net/http"
	"strconv"

	"spotter/ent"
	"spotter/ent/playlist"
	"spotter/ent/playlisttrack"
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

	// Get tracks for this playlist from the playlist_tracks table
	playlistTracks, err := h.Client.PlaylistTrack.Query().
		Where(playlisttrack.HasPlaylistWith(playlist.ID(playlistID))).
		WithTrack(func(q *ent.TrackQuery) {
			q.WithArtist()
			q.WithAlbum(func(aq *ent.AlbumQuery) {
				aq.WithImages()
			})
		}).
		WithArtist().
		WithAlbum(func(q *ent.AlbumQuery) {
			q.WithImages()
		}).
		Order(ent.Asc(playlisttrack.FieldPosition)).
		All(r.Context())
	if err != nil {
		h.Logger.Error("failed to get playlist tracks", "error", err)
		playlistTracks = []*ent.PlaylistTrack{}
	}

	// Convert to TrackTableRow for the component
	rows := h.playlistTracksToRows(playlistTracks)

	h.Render(w, r, playlists.Show(pl, rows, h.Config))
}

// playlistTracksToRows converts playlist tracks to TrackTableRow for the track table component
func (h *Handler) playlistTracksToRows(tracks []*ent.PlaylistTrack) []components.TrackTableRow {
	rows := make([]components.TrackTableRow, len(tracks))
	for i, pt := range tracks {
		row := components.TrackTableRow{
			Index:              i + 1,
			ExplicitTrackName:  pt.TrackName,
			ExplicitArtistName: pt.ArtistName,
			ExplicitAlbumName:  pt.AlbumName,
			ExplicitDurationMs: pt.DurationMs,
		}

		// If linked to catalog track, use enriched data
		if pt.Edges.Track != nil {
			row.Track = pt.Edges.Track
		}
		// If linked to catalog artist, set ID for linking
		if pt.Edges.Artist != nil {
			row.ExplicitArtistID = pt.Edges.Artist.ID
		}
		// If linked to catalog album, set ID for linking
		if pt.Edges.Album != nil {
			row.ExplicitAlbumID = pt.Edges.Album.ID
		}

		rows[i] = row
	}
	return rows
}
