package handlers

import (
	"bytes"
	"fmt"
	"net/http"

	"spotter/ent"
	"spotter/internal/events"
	"spotter/internal/views/components"
)

func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	u := h.GetUser(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}
	flusher.Flush()

	eventChan, cancel := h.Bus.Subscribe(u.ID)
	defer cancel()

	ctx := r.Context()

	for {
		select {
		case <-ctx.Done():
			return
		case event := <-eventChan:
			var buf bytes.Buffer

			switch event.Type {
			case events.EventTypeRecentListen:
				if listen, ok := event.Payload.(*ent.Listen); ok {
					row := components.TrackTableRow{
						Listen: listen,
					}
					if err := components.TrackTableRowRender(row, []string{"source", "played_at", "track", "artist", "album"}, 0).Render(ctx, &buf); err != nil {
						h.Logger.Error("failed to render recent listen", "error", err)
						continue
					}
				}
			case events.EventTypeNotification:
				if payload, ok := event.Payload.(events.NotificationPayload); ok {
					if err := components.Toast(payload.Title, payload.Message, payload.IconType).Render(ctx, &buf); err != nil {
						h.Logger.Error("failed to render notification", "error", err)
						continue
					}
				}
			}

			if buf.Len() > 0 {
				fmt.Fprintf(w, "event: %s\n", event.Type)
				// Write data line by line to adhere to SSE spec
				lines := bytes.Split(buf.Bytes(), []byte("\n"))
				for _, line := range lines {
					if len(line) > 0 {
						fmt.Fprintf(w, "data: %s\n", line)
					}
				}
				fmt.Fprintf(w, "\n")
				flusher.Flush()
			}
		}
	}
}
