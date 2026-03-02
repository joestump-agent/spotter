package handlers

import "net/http"

// HTMXEvent sets the HX-Trigger response header and writes the status code.
func (h *Handler) HTMXEvent(w http.ResponseWriter, event string, statusCode int) {
	w.Header().Set("HX-Trigger", event)
	w.WriteHeader(statusCode)
}
