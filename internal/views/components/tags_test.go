package components

// Governing: SPEC-0014 REQ "UI Tag Visual Differentiation"

import (
	"context"
	"strings"
	"testing"

	"spotter/ent"
	"spotter/ent/tag"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderTypedTagBadge renders the TypedTagBadge component to a string.
func renderTypedTagBadge(t *testing.T, tv TagView) string {
	t.Helper()
	var sb strings.Builder
	require.NoError(t, TypedTagBadge(tv).Render(context.Background(), &sb))
	return sb.String()
}

// TestTypedTagBadge_AITags verifies AI tags get the accent badge with the
// sparkles icon, per SPEC-0014 REQ "UI Tag Visual Differentiation".
func TestTypedTagBadge_AITags(t *testing.T) {
	html := renderTypedTagBadge(t, TagView{Name: "Ethereal", TagType: "ai"})
	assert.Contains(t, html, "badge-accent")
	assert.Contains(t, html, "heroicons--sparkles")
	assert.Contains(t, html, "Ethereal")
}

// TestTypedTagBadge_TypeStyles spot-checks the non-AI badge variants.
func TestTypedTagBadge_TypeStyles(t *testing.T) {
	tests := []struct {
		tagType   string
		wantClass string
		wantIcon  string
	}{
		{"genre", "badge-primary", "heroicons--tag"},
		{"label", "badge-secondary", "heroicons--building-office"},
		{"source", "badge-info", "heroicons--server"},
		{"id3", "badge-neutral", "heroicons--musical-note"},
	}
	for _, tt := range tests {
		t.Run(tt.tagType, func(t *testing.T) {
			html := renderTypedTagBadge(t, TagView{Name: "x", TagType: tt.tagType})
			assert.Contains(t, html, tt.wantClass)
			assert.Contains(t, html, tt.wantIcon)
		})
	}
}

// TestTagViewsFromEnt verifies conversion, ordering (genre first, AI last),
// and skipping of nil/empty entries.
func TestTagViewsFromEnt(t *testing.T) {
	entTags := []*ent.Tag{
		{Name: "Ethereal", TagType: tag.TagTypeAi},
		{Name: "Noise Pop", TagType: tag.TagTypeId3},
		nil,
		{Name: "Shoegaze", TagType: tag.TagTypeGenre},
		{Name: "", TagType: tag.TagTypeGenre},
		{Name: "4AD", TagType: tag.TagTypeLabel},
	}

	views := TagViewsFromEnt(entTags)
	require.Len(t, views, 4)
	assert.Equal(t, TagView{Name: "Shoegaze", TagType: "genre"}, views[0])
	assert.Equal(t, TagView{Name: "4AD", TagType: "label"}, views[1])
	assert.Equal(t, TagView{Name: "Noise Pop", TagType: "id3"}, views[2])
	assert.Equal(t, TagView{Name: "Ethereal", TagType: "ai"}, views[3])
}
