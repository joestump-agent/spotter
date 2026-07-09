package notifications

import (
	"fmt"
	"strings"
	"testing"
)

// Governing: SPEC-0015 REQ "Email Content" — provider display names
func TestProviderDisplayName(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"spotify", "Spotify"},
		{"lastfm", "Last.fm"},
		{"navidrome", "Navidrome"},
		{"unknown-provider", "unknown-provider"}, // fall back to raw key
	}
	for _, tt := range tests {
		if got := providerDisplayName(tt.key); got != tt.want {
			t.Errorf("providerDisplayName(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

// Governing: SPEC-0015 REQ "Email Content" — spec subject format and display names
func TestBuildEmail_SubjectAndBody(t *testing.T) {
	subject, body := buildEmail("lastfm", fmt.Errorf("boom"), "http://spotter.example.com", 7)

	wantSubject := "[Spotter] Last.fm sync error — action required"
	if subject != wantSubject {
		t.Errorf("subject = %q, want %q", subject, wantSubject)
	}
	if strings.Contains(subject, "lastfm") || strings.Contains(body, "lastfm") {
		t.Error("raw provider key should not appear in subject or body")
	}
	if !strings.Contains(body, "Last.fm") {
		t.Error("body should contain the provider display name")
	}
	// Governing: SPEC-0015 REQ "Email Content" — action link points at the Spotter instance
	if !strings.Contains(body, "http://spotter.example.com/preferences/providers") {
		t.Errorf("body should link to the Spotter preferences URL, got: %q", body)
	}
}
