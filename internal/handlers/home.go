package handlers

import (
	"net/http"
	"time"

	"spotter/internal/views/home"
)

func (h *Handler) Home(w http.ResponseWriter, r *http.Request) {
	h.Render(w, r, home.Index())
}

func (h *Handler) GeneratePlaylist(w http.ResponseWriter, r *http.Request) {
	prompt := r.FormValue("prompt")

	// TODO: Integrate with AI service to generate playlist
	// For now, we just simulate a delay and return a success message or dummy list.
	// The requirement says "It should then pull in all recent listening data from Spotify and Navidrome."
	// This probably happens in the background or as part of the context for the AI.

	h.Logger.Info("Generating playlist", "prompt", prompt)

	// Simulate work
	time.Sleep(2 * time.Second)

	w.Write([]byte("<div class=\"p-4 mb-4 text-sm text-green-800 rounded-lg bg-green-50 dark:bg-gray-800 dark:text-green-400\" role=\"alert\"><span class=\"font-medium\">Success!</span> Playlist generation started based on prompt: \"" + prompt + "\". Check Navidrome shortly.</div>"))
}
