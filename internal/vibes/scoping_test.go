// Tests for REQ-VIBES-012 (min_tracks lower bound) and REQ-VIBES-022
// (user-scoped candidate tracks in the enhancer). Issue #13.
package vibes

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"spotter/ent"
	"spotter/ent/enttest"
	"spotter/internal/config"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Governing: SPEC vibes-ai-mixtape-engine REQ-VIBES-012
func TestResolveMaxTracks_EnforcesMinTracks(t *testing.T) {
	tests := []struct {
		name      string
		min       int
		max       int
		def       int
		mixtape   int
		requested int
		expected  int
	}{
		{
			name: "below min is clamped up",
			min:  5, max: 100, def: 25,
			mixtape:  2,
			expected: 5,
		},
		{
			name: "above min is untouched",
			min:  5, max: 100, def: 25,
			mixtape:  10,
			expected: 10,
		},
		{
			name: "request override below min is clamped up",
			min:  8, max: 100, def: 25,
			mixtape: 50, requested: 3,
			expected: 8,
		},
		{
			name: "above max is clamped down",
			min:  5, max: 40, def: 25,
			mixtape:  99,
			expected: 40,
		},
		{
			name: "zero falls back to default",
			min:  5, max: 100, def: 25,
			mixtape:  0,
			expected: 25,
		},
		{
			name: "default below min is clamped up",
			min:  30, max: 100, def: 25,
			mixtape:  0,
			expected: 30,
		},
		{
			name: "unset min applies no lower bound",
			min:  0, max: 100, def: 25,
			mixtape:  1,
			expected: 1,
		},
		{
			name: "misconfigured min above max still respects hard cap",
			min:  50, max: 40, def: 25,
			mixtape:  2,
			expected: 40,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Vibes.MinTracks = tt.min
			cfg.Vibes.MaxTracks = tt.max
			cfg.Vibes.DefaultMaxTracks = tt.def

			g := NewMixtapeGenerator(nil, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
			req := &GenerationRequest{
				Mixtape:   &ent.Mixtape{MaxTracks: tt.mixtape},
				MaxTracks: tt.requested,
			}
			assert.Equal(t, tt.expected, g.resolveMaxTracks(req))
		})
	}
}

// Governing: SPEC vibes-ai-mixtape-engine REQ-VIBES-022 — the enhancer's
// candidate pool must be limited to the requesting user's library.
func TestEnhancerGetAvailableTracks_ScopedToUser(t *testing.T) {
	dbName := strings.NewReplacer("/", "_", " ", "_", "=", "_").Replace(t.Name())
	client := enttest.Open(t, "sqlite3", "file:"+dbName+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	ctx := context.Background()

	u1, err := client.User.Create().SetUsername("enhancer_scope_u1").SetPaginationSize(25).Save(ctx)
	require.NoError(t, err)
	u2, err := client.User.Create().SetUsername("enhancer_scope_u2").SetPaginationSize(25).Save(ctx)
	require.NoError(t, err)

	makeTrack := func(u *ent.User, artistName, trackName string) {
		a, err := client.Artist.Create().SetName(artistName).SetUser(u).Save(ctx)
		require.NoError(t, err)
		_, err = client.Track.Create().SetName(trackName).SetArtist(a).Save(ctx)
		require.NoError(t, err)
	}

	makeTrack(u1, "U1 Artist", "U1 Track")
	makeTrack(u2, "U2 Artist", "U2 Track")

	cfg := &config.Config{}
	e := NewPlaylistEnhancer(client, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	available, err := e.getAvailableTracks(ctx, u1.ID, map[int]bool{})
	require.NoError(t, err)

	require.Len(t, available, 1, "only the requesting user's tracks may be candidates")
	assert.Equal(t, "U1 Track", available[0].Name)
	assert.Equal(t, "U1 Artist", available[0].Artist)
}
