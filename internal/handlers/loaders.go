package handlers

import (
	"context"

	"spotter/ent"
	"spotter/ent/dj"
	"spotter/ent/mixtape"
	"spotter/ent/playlist"
	"spotter/ent/user"
)

// GetPlaylistForUser retrieves a playlist by ID that belongs to the given user.
// Returns an ent.NotFoundError if not found or not owned by the user.
func (h *Handler) GetPlaylistForUser(ctx context.Context, playlistID, userID int) (*ent.Playlist, error) {
	return h.Client.Playlist.Query().
		Where(playlist.ID(playlistID), playlist.HasUserWith(user.ID(userID))).
		Only(ctx)
}

// GetDJForUser retrieves a DJ by ID that belongs to the given user.
// Returns an ent.NotFoundError if not found or not owned by the user.
func (h *Handler) GetDJForUser(ctx context.Context, djID, userID int) (*ent.DJ, error) {
	return h.Client.DJ.Query().
		Where(dj.ID(djID), dj.HasUserWith(user.ID(userID))).
		Only(ctx)
}

// GetMixtapeForUser retrieves a mixtape by ID that belongs to the given user.
// Returns an ent.NotFoundError if not found or not owned by the user.
func (h *Handler) GetMixtapeForUser(ctx context.Context, mixtapeID, userID int) (*ent.Mixtape, error) {
	return h.Client.Mixtape.Query().
		Where(mixtape.ID(mixtapeID), mixtape.HasUserWith(user.ID(userID))).
		Only(ctx)
}
