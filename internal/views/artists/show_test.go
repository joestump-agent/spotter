package artists

// Governing: SPEC-0014 REQ "UI Tag Visual Differentiation"

import (
	"context"
	"strings"
	"testing"

	"spotter/ent"
	"spotter/ent/tag"
	"spotter/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderShow renders the artist Show page for the given artist.
func renderShow(t *testing.T, artist *ent.Artist) string {
	t.Helper()
	var sb strings.Builder
	err := Show(artist, nil, nil, &ArtistStats{}, nil, nil, &config.Config{}, "30d").
		Render(context.Background(), &sb)
	require.NoError(t, err)
	return sb.String()
}

// TestShow_RendersTypedTagBadges verifies that loaded Tag entities render via
// TypedTagBadge (AI tags get badge-accent + sparkles) instead of the legacy
// JSON-field badges.
func TestShow_RendersTypedTagBadges(t *testing.T) {
	artist := &ent.Artist{
		ID:     1,
		Name:   "My Bloody Valentine",
		Genres: []string{"legacy-genre"},
		Edges: ent.ArtistEdges{
			TagEntities: []*ent.Tag{
				{Name: "Shoegaze", TagType: tag.TagTypeGenre},
				{Name: "Ethereal", TagType: tag.TagTypeAi},
			},
		},
	}

	html := renderShow(t, artist)
	assert.Contains(t, html, "Shoegaze")
	assert.Contains(t, html, "Ethereal")
	assert.Contains(t, html, "badge-accent")
	assert.Contains(t, html, "heroicons--sparkles")
	assert.NotContains(t, html, "legacy-genre", "legacy fields must not render when Tag entities exist")
}

// TestShow_FallsBackToLegacyFields verifies the migration-window fallback:
// with no Tag entities loaded, the legacy JSON fields still render.
func TestShow_FallsBackToLegacyFields(t *testing.T) {
	artist := &ent.Artist{
		ID:     1,
		Name:   "My Bloody Valentine",
		Genres: []string{"legacy-genre"},
		AiTags: []string{"legacy-ai"},
	}

	html := renderShow(t, artist)
	assert.Contains(t, html, "legacy-genre")
	assert.Contains(t, html, "legacy-ai")
}
