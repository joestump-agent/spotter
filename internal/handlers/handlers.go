package handlers

import (
	"context"
	"log/slog"
	"net/http"

	"spotter/ent"
	"spotter/internal/config"
	"spotter/internal/events"
	"spotter/internal/services"

	"github.com/a-h/templ"
)

type contextKey string

const UserContextKey contextKey = "user"

type Handler struct {
	Client          *ent.Client
	Config          *config.Config
	Logger          *slog.Logger
	Syncer          *services.Syncer
	MetadataSvc     *services.MetadataService
	PlaylistSyncSvc *services.PlaylistSyncService
	Bus             *events.Bus
}

func New(client *ent.Client, cfg *config.Config, logger *slog.Logger, syncer *services.Syncer, metadataSvc *services.MetadataService, playlistSyncSvc *services.PlaylistSyncService, bus *events.Bus) *Handler {
	return &Handler{
		Client:          client,
		Config:          cfg,
		Logger:          logger,
		Syncer:          syncer,
		MetadataSvc:     metadataSvc,
		PlaylistSyncSvc: playlistSyncSvc,
		Bus:             bus,
	}
}

func (h *Handler) Render(w http.ResponseWriter, r *http.Request, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := component.Render(r.Context(), w); err != nil {
		h.Logger.Error("failed to render component", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (h *Handler) GetUser(ctx context.Context) *ent.User {
	u, ok := ctx.Value(UserContextKey).(*ent.User)
	if !ok {
		return nil
	}
	return u
}
