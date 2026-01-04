package handlers

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"spotter/ent"
	"spotter/ent/artist"
	"spotter/ent/dj"
	"spotter/ent/listen"
	"spotter/ent/mixtape"
	"spotter/ent/user"
	"spotter/internal/views/vibes"

	"github.com/go-chi/chi/v5"
)

// VibesRedirect redirects to the DJs page
func (h *Handler) VibesRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/vibes/djs", http.StatusSeeOther)
}

// DJsIndex shows the list of DJs
func (h *Handler) DJsIndex(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	djs, err := h.Client.DJ.Query().
		Where(dj.HasUserWith(user.ID(u.ID))).
		WithMixtapes().
		Order(ent.Desc(dj.FieldCreatedAt)).
		All(r.Context())
	if err != nil {
		h.Logger.Error("failed to query DJs", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	h.Render(w, r, vibes.DJsIndex(djs, h.Config))
}

// DJShow shows a single DJ
func (h *Handler) DJShow(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	djID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "Invalid DJ ID", http.StatusBadRequest)
		return
	}

	// Get the DJ with mixtapes
	d, err := h.Client.DJ.Query().
		Where(
			dj.ID(djID),
			dj.HasUserWith(user.ID(u.ID)),
		).
		WithMixtapes().
		Only(r.Context())
	if err != nil {
		h.Logger.Error("failed to get DJ", "error", err, "id", djID)
		http.Error(w, "DJ not found", http.StatusNotFound)
		return
	}

	h.Render(w, r, vibes.DJShow(d, h.Config))
}

// CreateDJ creates a new DJ
func (h *Handler) CreateDJ(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	systemPrompt := strings.TrimSpace(r.FormValue("system_prompt"))
	genresInclude := parseCommaSeparated(r.FormValue("genres_include"))
	genresExclude := parseCommaSeparated(r.FormValue("genres_exclude"))
	vibesTags := parseCommaSeparated(r.FormValue("vibes"))
	artistsInclude := parseCommaSeparated(r.FormValue("artists_include"))
	artistsExclude := parseCommaSeparated(r.FormValue("artists_exclude"))

	_, err := h.Client.DJ.Create().
		SetName(name).
		SetSystemPrompt(systemPrompt).
		SetGenresInclude(genresInclude).
		SetGenresExclude(genresExclude).
		SetVibes(vibesTags).
		SetArtistsInclude(artistsInclude).
		SetArtistsExclude(artistsExclude).
		SetUser(u).
		Save(r.Context())

	if err != nil {
		h.Logger.Error("failed to create DJ", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Trigger", "dj-created")
	w.WriteHeader(http.StatusOK)
}

// UpdateDJ updates an existing DJ
func (h *Handler) UpdateDJ(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	djID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "Invalid DJ ID", http.StatusBadRequest)
		return
	}

	// Verify ownership
	d, err := h.Client.DJ.Query().
		Where(dj.ID(djID), dj.HasUserWith(user.ID(u.ID))).
		Only(r.Context())
	if err != nil {
		http.Error(w, "DJ not found", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	systemPrompt := strings.TrimSpace(r.FormValue("system_prompt"))
	genresInclude := parseCommaSeparated(r.FormValue("genres_include"))
	genresExclude := parseCommaSeparated(r.FormValue("genres_exclude"))
	vibesTags := parseCommaSeparated(r.FormValue("vibes"))
	artistsInclude := parseCommaSeparated(r.FormValue("artists_include"))
	artistsExclude := parseCommaSeparated(r.FormValue("artists_exclude"))

	_, err = h.Client.DJ.UpdateOne(d).
		SetName(name).
		SetSystemPrompt(systemPrompt).
		SetGenresInclude(genresInclude).
		SetGenresExclude(genresExclude).
		SetVibes(vibesTags).
		SetArtistsInclude(artistsInclude).
		SetArtistsExclude(artistsExclude).
		Save(r.Context())

	if err != nil {
		h.Logger.Error("failed to update DJ", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Trigger", "dj-updated")
	w.WriteHeader(http.StatusOK)
}

// DeleteDJ deletes a DJ
func (h *Handler) DeleteDJ(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	djID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "Invalid DJ ID", http.StatusBadRequest)
		return
	}

	// Verify ownership
	_, err = h.Client.DJ.Query().
		Where(dj.ID(djID), dj.HasUserWith(user.ID(u.ID))).
		Only(r.Context())
	if err != nil {
		http.Error(w, "DJ not found", http.StatusNotFound)
		return
	}

	err = h.Client.DJ.DeleteOneID(djID).Exec(r.Context())
	if err != nil {
		h.Logger.Error("failed to delete DJ", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Trigger", "dj-deleted")
	w.WriteHeader(http.StatusOK)
}

// MixtapesIndex shows the list of Mixtapes
func (h *Handler) MixtapesIndex(w http.ResponseWriter, r *http.Request) {
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

	// Query mixtapes with pagination
	mixtapes, err := h.Client.Mixtape.Query().
		Where(mixtape.HasUserWith(user.ID(u.ID))).
		WithDj().
		Order(ent.Desc(mixtape.FieldUpdatedAt)).
		Limit(pageSize).
		Offset(offset).
		All(r.Context())
	if err != nil {
		h.Logger.Error("failed to query mixtapes", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Get total count for pagination
	total, err := h.Client.Mixtape.Query().
		Where(mixtape.HasUserWith(user.ID(u.ID))).
		Count(r.Context())
	if err != nil {
		h.Logger.Error("failed to count mixtapes", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	totalPages := (total + pageSize - 1) / pageSize

	// Get all DJs for the create modal
	djs, err := h.Client.DJ.Query().
		Where(dj.HasUserWith(user.ID(u.ID))).
		Order(ent.Asc(dj.FieldName)).
		All(r.Context())
	if err != nil {
		h.Logger.Error("failed to query DJs", "error", err)
		djs = []*ent.DJ{}
	}

	h.Render(w, r, vibes.MixtapesIndex(mixtapes, djs, page, totalPages, h.Config))
}

// CreateMixtape creates a new Mixtape
func (h *Handler) CreateMixtape(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	description := strings.TrimSpace(r.FormValue("description"))
	schedule := r.FormValue("schedule")
	if schedule == "" {
		schedule = "none"
	}

	djID, err := strconv.Atoi(r.FormValue("dj_id"))
	if err != nil {
		http.Error(w, "DJ is required", http.StatusBadRequest)
		return
	}

	// Verify DJ ownership
	d, err := h.Client.DJ.Query().
		Where(dj.ID(djID), dj.HasUserWith(user.ID(u.ID))).
		Only(r.Context())
	if err != nil {
		http.Error(w, "DJ not found", http.StatusNotFound)
		return
	}

	syncToNavidrome := r.FormValue("sync_to_navidrome") == "on"

	maxTracks := 25 // default
	if maxTracksStr := r.FormValue("max_tracks"); maxTracksStr != "" {
		if mt, err := strconv.Atoi(maxTracksStr); err == nil && mt >= 1 && mt <= 100 {
			maxTracks = mt
		}
	}

	_, err = h.Client.Mixtape.Create().
		SetName(name).
		SetDescription(description).
		SetSchedule(mixtape.Schedule(schedule)).
		SetMaxTracks(maxTracks).
		SetSyncToNavidrome(syncToNavidrome).
		SetDj(d).
		SetUser(u).
		Save(r.Context())

	if err != nil {
		h.Logger.Error("failed to create mixtape", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Trigger", "mixtape-created")
	w.WriteHeader(http.StatusOK)
}

// UpdateMixtape updates an existing Mixtape
func (h *Handler) UpdateMixtape(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	mixtapeID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "Invalid Mixtape ID", http.StatusBadRequest)
		return
	}

	// Verify ownership
	m, err := h.Client.Mixtape.Query().
		Where(mixtape.ID(mixtapeID), mixtape.HasUserWith(user.ID(u.ID))).
		Only(r.Context())
	if err != nil {
		http.Error(w, "Mixtape not found", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	description := strings.TrimSpace(r.FormValue("description"))
	schedule := r.FormValue("schedule")
	if schedule == "" {
		schedule = "none"
	}

	djID, err := strconv.Atoi(r.FormValue("dj_id"))
	if err != nil {
		http.Error(w, "DJ is required", http.StatusBadRequest)
		return
	}

	// Verify DJ ownership
	d, err := h.Client.DJ.Query().
		Where(dj.ID(djID), dj.HasUserWith(user.ID(u.ID))).
		Only(r.Context())
	if err != nil {
		http.Error(w, "DJ not found", http.StatusNotFound)
		return
	}

	syncToNavidrome := r.FormValue("sync_to_navidrome") == "on"

	maxTracks := 25 // default
	if maxTracksStr := r.FormValue("max_tracks"); maxTracksStr != "" {
		if mt, err := strconv.Atoi(maxTracksStr); err == nil && mt >= 1 && mt <= 100 {
			maxTracks = mt
		}
	}

	_, err = h.Client.Mixtape.UpdateOne(m).
		SetName(name).
		SetDescription(description).
		SetSchedule(mixtape.Schedule(schedule)).
		SetMaxTracks(maxTracks).
		SetSyncToNavidrome(syncToNavidrome).
		SetDj(d).
		Save(r.Context())

	if err != nil {
		h.Logger.Error("failed to update mixtape", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Trigger", "mixtape-updated")
	w.WriteHeader(http.StatusOK)
}

// DeleteMixtape deletes a Mixtape
func (h *Handler) DeleteMixtape(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	mixtapeID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "Invalid Mixtape ID", http.StatusBadRequest)
		return
	}

	// Verify ownership
	_, err = h.Client.Mixtape.Query().
		Where(mixtape.ID(mixtapeID), mixtape.HasUserWith(user.ID(u.ID))).
		Only(r.Context())
	if err != nil {
		http.Error(w, "Mixtape not found", http.StatusNotFound)
		return
	}

	// TODO: If synced to Navidrome, delete the playlist there too

	err = h.Client.Mixtape.DeleteOneID(mixtapeID).Exec(r.Context())
	if err != nil {
		h.Logger.Error("failed to delete mixtape", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Trigger", "mixtape-deleted")
	w.WriteHeader(http.StatusOK)
}

// ToggleMixtapeSync toggles the Navidrome sync status of a Mixtape
func (h *Handler) ToggleMixtapeSync(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	mixtapeID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "Invalid Mixtape ID", http.StatusBadRequest)
		return
	}

	// Verify ownership and get current state
	m, err := h.Client.Mixtape.Query().
		Where(mixtape.ID(mixtapeID), mixtape.HasUserWith(user.ID(u.ID))).
		Only(r.Context())
	if err != nil {
		http.Error(w, "Mixtape not found", http.StatusNotFound)
		return
	}

	newSyncState := !m.SyncToNavidrome

	_, err = h.Client.Mixtape.UpdateOne(m).
		SetSyncToNavidrome(newSyncState).
		Save(r.Context())

	if err != nil {
		h.Logger.Error("failed to toggle mixtape sync", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// TODO: If enabling sync, trigger sync to Navidrome
	// TODO: If disabling sync, optionally remove from Navidrome

	w.Header().Set("HX-Trigger", "mixtape-sync-toggled")
	w.WriteHeader(http.StatusOK)
}

// GenerateMixtape triggers AI generation of tracks for a Mixtape
func (h *Handler) GenerateMixtape(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	mixtapeID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "Invalid Mixtape ID", http.StatusBadRequest)
		return
	}

	// Verify ownership
	m, err := h.Client.Mixtape.Query().
		Where(mixtape.ID(mixtapeID), mixtape.HasUserWith(user.ID(u.ID))).
		WithDj().
		Only(r.Context())
	if err != nil {
		http.Error(w, "Mixtape not found", http.StatusNotFound)
		return
	}

	h.Logger.Info("generating mixtape", "mixtape_id", m.ID, "name", m.Name, "dj", m.Edges.Dj.Name)

	// TODO: Implement actual AI generation using the DJ's settings
	// For now, just update the last generated timestamp

	_, err = h.Client.Mixtape.UpdateOne(m).
		SetLastGeneratedAt(time.Now()).
		Save(r.Context())
	if err != nil {
		h.Logger.Error("failed to update mixtape last generated time", "error", err)
	}

	// Just acknowledge for now - actual implementation will come later
	w.Header().Set("HX-Trigger", "mixtape-generated")
	w.WriteHeader(http.StatusOK)
}

// MixtapeShow shows a single Mixtape
func (h *Handler) MixtapeShow(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	mixtapeID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "Invalid Mixtape ID", http.StatusBadRequest)
		return
	}

	// Get the mixtape with DJ
	m, err := h.Client.Mixtape.Query().
		Where(
			mixtape.ID(mixtapeID),
			mixtape.HasUserWith(user.ID(u.ID)),
		).
		WithDj().
		Only(r.Context())
	if err != nil {
		h.Logger.Error("failed to get mixtape", "error", err, "id", mixtapeID)
		http.Error(w, "Mixtape not found", http.StatusNotFound)
		return
	}

	// Get all DJs for the edit modal
	djs, err := h.Client.DJ.Query().
		Where(dj.HasUserWith(user.ID(u.ID))).
		Order(ent.Asc(dj.FieldName)).
		All(r.Context())
	if err != nil {
		h.Logger.Error("failed to query DJs", "error", err)
		djs = []*ent.DJ{}
	}

	h.Render(w, r, vibes.MixtapeShow(m, djs, h.Config))
}

// GenreSuggestions returns unique genres from the user's listen history
func (h *Handler) GenreSuggestions(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	// Get all listens for this user with their tracks and artists
	listens, err := h.Client.Listen.Query().
		Where(listen.HasUserWith(user.ID(u.ID))).
		WithTrack().
		WithArtist().
		All(r.Context())
	if err != nil {
		h.Logger.Error("failed to query listens for genre suggestions", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Collect unique genres from tracks and artists
	genreSet := make(map[string]struct{})
	for _, l := range listens {
		if l.Edges.Track != nil {
			for _, g := range l.Edges.Track.Genres {
				genreSet[g] = struct{}{}
			}
		}
		if l.Edges.Artist != nil {
			for _, g := range l.Edges.Artist.Genres {
				genreSet[g] = struct{}{}
			}
		}
	}

	// Convert to slice and filter by query
	var genres []string
	for g := range genreSet {
		if query == "" || strings.Contains(strings.ToLower(g), query) {
			genres = append(genres, g)
		}
	}

	// Sort alphabetically
	sort.Strings(genres)

	// Limit results
	if len(genres) > 50 {
		genres = genres[:50]
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(genres)
}

// ArtistSuggestions returns unique artist names from the user's listen history
func (h *Handler) ArtistSuggestions(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	// Get all artists for this user
	artists, err := h.Client.Artist.Query().
		Where(artist.HasUserWith(user.ID(u.ID))).
		Order(ent.Asc(artist.FieldName)).
		All(r.Context())
	if err != nil {
		h.Logger.Error("failed to query artists for suggestions", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Filter by query and collect names
	var artistNames []string
	for _, a := range artists {
		if query == "" || strings.Contains(strings.ToLower(a.Name), query) {
			artistNames = append(artistNames, a.Name)
		}
	}

	// Limit results
	if len(artistNames) > 50 {
		artistNames = artistNames[:50]
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(artistNames)
}

// parseCommaSeparated parses a comma-separated string into a slice of trimmed strings
func parseCommaSeparated(s string) []string {
	if s == "" {
		return nil
	}

	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}

	if len(result) == 0 {
		return nil
	}
	return result
}
