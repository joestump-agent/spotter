package handlers

import (
	"fmt"
	"net/http"
)

// NotFound sends a 404 response indicating the entity was not found.
func (h *Handler) NotFound(w http.ResponseWriter, entity string) {
	http.Error(w, fmt.Sprintf("%s not found", entity), http.StatusNotFound)
}

// InvalidParam sends a 400 response indicating the parameter is invalid.
func (h *Handler) InvalidParam(w http.ResponseWriter, name string) {
	http.Error(w, fmt.Sprintf("Invalid %s", name), http.StatusBadRequest)
}

// Unauthorized sends a 401 Unauthorized response.
func (h *Handler) Unauthorized(w http.ResponseWriter) {
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}
