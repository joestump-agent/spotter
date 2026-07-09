package services

// Tests for enrichment merge semantics and external-ID constraint handling:
//   - REQ-ENRICH-020: later enrichers must not overwrite fields set by earlier
//     enrichers in the same pass (first in config order wins). The old guards
//     compared against the stale entity value, so the LAST enricher won.
//   - Per-user external-ID uniqueness: a unique-constraint failure on
//     musicbrainz_id/spotify_id must not discard the whole update (including
//     SetLastEnrichedAt).
//
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-020, REQ-ENRICH-013

import (
	"context"
	"testing"

	"spotter/ent"
	"spotter/internal/enrichers"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubArtistEnricher is a configurable fake implementing enrichers.ArtistEnricher.
type stubArtistEnricher struct {
	name string
	data *enrichers.ArtistData
}

func (s *stubArtistEnricher) Type() enrichers.Type { return enrichers.Type(s.name) }
func (s *stubArtistEnricher) Name() string         { return s.name }
func (s *stubArtistEnricher) IsAvailable() bool    { return true }

func (s *stubArtistEnricher) EnrichArtist(ctx context.Context, artist *ent.Artist) (*enrichers.ArtistData, error) {
	return s.data, nil
}

func (s *stubArtistEnricher) GetArtistImages(ctx context.Context, artist *ent.Artist) ([]enrichers.ImageData, error) {
	return nil, nil
}

// TestEnrichArtist_FirstEnricherWins verifies that when two enrichers both
// return a value for the same field, the FIRST one in config order wins and
// the later one cannot overwrite it.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-020
func TestEnrichArtist_FirstEnricherWins(t *testing.T) {
	svc := newTestMetadataService(t)
	ctx := context.Background()

	u, err := svc.client.User.Create().SetUsername("testuser").SetTheme("dark").Save(ctx)
	require.NoError(t, err)

	art, err := svc.client.Artist.Create().SetName("Test Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)

	first := &stubArtistEnricher{
		name: "first",
		data: &enrichers.ArtistData{
			Bio:      "bio from first enricher",
			SortName: "Artist, Test",
		},
	}
	second := &stubArtistEnricher{
		name: "second",
		data: &enrichers.ArtistData{
			Bio:       "bio from second enricher",
			LastFMURL: "https://last.fm/music/test", // untouched by first: should apply
		},
	}

	err = svc.enrichArtist(ctx, u, art, enrichers.List{first, second}, nil)
	require.NoError(t, err)

	got, err := svc.client.Artist.Get(ctx, art.ID)
	require.NoError(t, err)

	assert.Equal(t, "bio from first enricher", got.Bio,
		"first enricher in config order must win; later enrichers must not overwrite")
	assert.Equal(t, "Artist, Test", got.SortName)
	assert.Equal(t, "https://last.fm/music/test", got.LastfmURL,
		"fields untouched by earlier enrichers should still be filled by later ones")
	assert.NotNil(t, got.LastEnrichedAt)
}

// TestEnrichArtist_ExternalIDConflictKeepsRestOfUpdate verifies that a
// unique-constraint failure on an external ID (per-user unique
// musicbrainz_id) does not discard the rest of the update, including
// SetLastEnrichedAt.
func TestEnrichArtist_ExternalIDConflictKeepsRestOfUpdate(t *testing.T) {
	svc := newTestMetadataService(t)
	ctx := context.Background()

	u, err := svc.client.User.Create().SetUsername("testuser").SetTheme("dark").Save(ctx)
	require.NoError(t, err)

	// An existing artist for the same user already owns this MBID.
	_, err = svc.client.Artist.Create().
		SetName("Existing Artist").
		SetMusicbrainzID("11111111-1111-1111-1111-111111111111").
		SetUser(u).
		Save(ctx)
	require.NoError(t, err)

	art, err := svc.client.Artist.Create().SetName("Conflicting Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)

	e := &stubArtistEnricher{
		name: "conflicting",
		data: &enrichers.ArtistData{
			MusicBrainzID: "11111111-1111-1111-1111-111111111111", // duplicate for this user
			Bio:           "still valuable bio",
		},
	}

	err = svc.enrichArtist(ctx, u, art, enrichers.List{e}, nil)
	require.NoError(t, err, "constraint failure on external ID should be retried, not bubble up")

	got, err := svc.client.Artist.Get(ctx, art.ID)
	require.NoError(t, err)

	assert.Empty(t, got.MusicbrainzID, "conflicting external ID must be dropped")
	assert.Equal(t, "still valuable bio", got.Bio, "non-ID fields must survive the retry")
	assert.NotNil(t, got.LastEnrichedAt, "LastEnrichedAt must survive the retry")
}

// TestArtistExternalIDs_UniquePerUserNotGlobally verifies the schema change:
// artists are per-user rows, so the same MBID/Spotify ID may exist for
// different users, but not twice for the same user.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-040 (per-user catalog entities)
func TestArtistExternalIDs_UniquePerUserNotGlobally(t *testing.T) {
	svc := newTestMetadataService(t)
	ctx := context.Background()

	u1, err := svc.client.User.Create().SetUsername("user1").SetTheme("dark").Save(ctx)
	require.NoError(t, err)
	u2, err := svc.client.User.Create().SetUsername("user2").SetTheme("dark").Save(ctx)
	require.NoError(t, err)

	const mbid = "22222222-2222-2222-2222-222222222222"

	_, err = svc.client.Artist.Create().SetName("Shared Artist").SetMusicbrainzID(mbid).SetUser(u1).Save(ctx)
	require.NoError(t, err)

	// A different user may hold the same external ID.
	_, err = svc.client.Artist.Create().SetName("Shared Artist").SetMusicbrainzID(mbid).SetUser(u2).Save(ctx)
	require.NoError(t, err, "same MBID must be allowed for a different user")

	// The same user may not hold it twice.
	_, err = svc.client.Artist.Create().SetName("Duplicate Artist").SetMusicbrainzID(mbid).SetUser(u1).Save(ctx)
	require.Error(t, err, "same MBID for the same user must violate the composite unique index")
	assert.True(t, ent.IsConstraintError(err))
}
