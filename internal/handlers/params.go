package handlers

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// ParseIntParam extracts a chi URL parameter and converts it to int.
// Writes a 400 response and returns (0, false) if missing or non-integer.
func (h *Handler) ParseIntParam(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	raw := chi.URLParam(r, name)
	val, err := strconv.Atoi(raw)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid %s", name), http.StatusBadRequest)
		return 0, false
	}
	return val, true
}
