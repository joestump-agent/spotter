package services

// Regression tests for issue spotter-6p7: AI enrichment re-trigger loop.
//
// The artist and track enrichment paths only stamped last_ai_enriched_at
// inside the `len(data.AITags) > 0` branch. When an AI enricher returned
// only AISummary (and/or AIBiography for artists) but no AITags, the
// timestamp was never set, so the openai enricher's skip check
// (internal/enrichers/openai/openai.go, `LastAiEnrichedAt` recency check)
// never engaged and those entities were re-enriched on every pass, forever.
//
// The album path was already fixed to stamp the timestamp "if any AI fields
// were set" (internal/services/metadata.go, enrichAlbum); these tests pin
// the same behavior for the artist and track paths.
//
// Governing: AGENTS.md ENR-AI-004 (skip recently enriched entities),
// SRV-AI-005 (update last_ai_enriched_at after successful AI enrichment)

import (
	"context"
	"testing"

	"spotter/ent"
	"spotter/internal/enrichers"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubTrackEnricher is a configurable fake implementing enrichers.TrackEnricher.
type stubTrackEnricher struct {
	name string
	data *enrichers.TrackData
}

func (s *stubTrackEnricher) Type() enrichers.Type { return enrichers.Type(s.name) }
func (s *stubTrackEnricher) Name() string         { return s.name }
func (s *stubTrackEnricher) IsAvailable() bool    { return true }

func (s *stubTrackEnricher) EnrichTrack(ctx context.Context, track *ent.Track) (*enrichers.TrackData, error) {
	return s.data, nil
}

// TestEnrichArtist_Regression_AISummaryOnlyStampsAiTimestamp verifies that
// last_ai_enriched_at is set when an enricher returns AI fields (AISummary,
// AIBiography) WITHOUT any AITags.
//
// Regression test for spotter-6p7: previously SetLastAiEnrichedAt was only
// called inside the `len(AITags) > 0` branch, so artists whose AI enrichment
// produced only a summary/biography were re-enriched forever.
func TestEnrichArtist_Regression_AISummaryOnlyStampsAiTimestamp(t *testing.T) {
	svc := newTestMetadataService(t)
	ctx := context.Background()

	u, err := svc.client.User.Create().SetUsername("testuser").SetTheme("dark").Save(ctx)
	require.NoError(t, err)

	art, err := svc.client.Artist.Create().SetName("AI Summary Only Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)

	e := &stubArtistEnricher{
		name: "ai-stub",
		data: &enrichers.ArtistData{
			AISummary:   "AI summary with no tags",
			AIBiography: "AI biography with no tags",
			// Deliberately no AITags — this is the bug trigger.
		},
	}

	err = svc.enrichArtist(ctx, u, art, enrichers.List{e}, nil)
	require.NoError(t, err)

	got, err := svc.client.Artist.Get(ctx, art.ID)
	require.NoError(t, err)

	assert.Equal(t, "AI summary with no tags", got.AiSummary)
	assert.Equal(t, "AI biography with no tags", got.AiBiography)
	assert.NotNil(t, got.LastAiEnrichedAt,
		"last_ai_enriched_at must be stamped when any AI field is set, "+
			"otherwise the openai enricher's skip check never engages and the artist is re-enriched forever (spotter-6p7)")
}

// TestEnrichTrack_Regression_AISummaryOnlyStampsAiTimestamp verifies that
// last_ai_enriched_at is set when a track enricher returns an AISummary
// WITHOUT any AITags.
//
// Regression test for spotter-6p7: previously SetLastAiEnrichedAt was only
// called inside the `len(AITags) > 0` branch, so tracks whose AI enrichment
// produced only a summary were re-enriched forever.
func TestEnrichTrack_Regression_AISummaryOnlyStampsAiTimestamp(t *testing.T) {
	svc := newTestMetadataService(t)
	ctx := context.Background()

	u, err := svc.client.User.Create().SetUsername("testuser").SetTheme("dark").Save(ctx)
	require.NoError(t, err)

	art, err := svc.client.Artist.Create().SetName("Test Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)

	tr, err := svc.client.Track.Create().SetName("AI Summary Only Track").SetArtist(art).Save(ctx)
	require.NoError(t, err)

	e := &stubTrackEnricher{
		name: "ai-stub",
		data: &enrichers.TrackData{
			AISummary: "AI track summary with no tags",
			// Deliberately no AITags — this is the bug trigger.
		},
	}

	err = svc.enrichTrack(ctx, u, tr, enrichers.List{e}, nil)
	require.NoError(t, err)

	got, err := svc.client.Track.Get(ctx, tr.ID)
	require.NoError(t, err)

	assert.Equal(t, "AI track summary with no tags", got.AiSummary)
	assert.NotNil(t, got.LastAiEnrichedAt,
		"last_ai_enriched_at must be stamped when any AI field is set, "+
			"otherwise the openai enricher's skip check never engages and the track is re-enriched forever (spotter-6p7)")
}
