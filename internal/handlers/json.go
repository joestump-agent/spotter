package handlers

import (
	"encoding/json"
	"net/http"
)

// RespondJSON writes a JSON response with the given status code.
func (h *Handler) RespondJSON(w http.ResponseWriter, statusCode int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		h.Logger.Error("failed to encode JSON response", "error", err)
	}
}
