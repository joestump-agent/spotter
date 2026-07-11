package enrichers

import (
	"context"
	"testing"

	"spotter/ent"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEnricher is a minimal Enricher implementation for registry/list tests.
type fakeEnricher struct {
	t Type
}

func (f *fakeEnricher) Type() Type        { return f.t }
func (f *fakeEnricher) Name() string      { return string(f.t) }
func (f *fakeEnricher) IsAvailable() bool { return true }

// fakeArtistEnricher additionally implements ArtistEnricher.
type fakeArtistEnricher struct {
	fakeEnricher
}

func (f *fakeArtistEnricher) EnrichArtist(ctx context.Context, artist *ent.Artist) (*ArtistData, error) {
	return nil, nil
}

func (f *fakeArtistEnricher) GetArtistImages(ctx context.Context, artist *ent.Artist) ([]ImageData, error) {
	return nil, nil
}

// fakeTrackEnricher additionally implements TrackEnricher.
type fakeTrackEnricher struct {
	fakeEnricher
}

func (f *fakeTrackEnricher) EnrichTrack(ctx context.Context, track *ent.Track) (*TrackData, error) {
	return nil, nil
}

// TestRegistryRegister_DuplicateReturnsError verifies that registering the
// same enricher type twice fails loudly instead of silently replacing the
// first factory.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-050/051
func TestRegistryRegister_DuplicateReturnsError(t *testing.T) {
	r := NewRegistry()

	factory := func(ctx context.Context, user *ent.User) (Enricher, error) {
		return &fakeEnricher{t: TypeSpotify}, nil
	}

	require.NoError(t, r.Register(TypeSpotify, factory))

	err := r.Register(TypeSpotify, factory)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")

	// A different type still registers fine.
	require.NoError(t, r.Register(TypeLastFM, factory))
}

// TestList_CapabilityAccessors verifies the capability accessors filter by
// interface while preserving config order.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-050/051 (listing by capability),
// SPEC metadata-enrichment-pipeline REQ-ENRICH-010 (deterministic config order)
func TestList_CapabilityAccessors(t *testing.T) {
	artistA := &fakeArtistEnricher{fakeEnricher{t: TypeMusicBrainz}}
	trackOnly := &fakeTrackEnricher{fakeEnricher{t: TypeNavidrome}}
	artistB := &fakeArtistEnricher{fakeEnricher{t: TypeSpotify}}
	baseOnly := &fakeEnricher{t: TypeFanart}

	list := List{artistA, trackOnly, artistB, baseOnly}

	artists := list.ArtistEnrichers()
	require.Len(t, artists, 2)
	assert.Equal(t, TypeMusicBrainz, artists[0].Type(), "config order must be preserved")
	assert.Equal(t, TypeSpotify, artists[1].Type())

	tracks := list.TrackEnrichers()
	require.Len(t, tracks, 1)
	assert.Equal(t, TypeNavidrome, tracks[0].Type())

	assert.Empty(t, list.AlbumEnrichers())
	assert.Empty(t, list.IDMatchers())
}

// TestParseType_ListenBrainz verifies the ListenBrainz enricher type parses
// from config strings alongside the existing types.
// Governing: SPEC metadata-enrichment-pipeline REQ "ListenBrainz Enricher" (REQ-ENRICH-060)
func TestParseType_ListenBrainz(t *testing.T) {
	parsed, ok := ParseType("listenbrainz")
	require.True(t, ok)
	assert.Equal(t, TypeListenBrainz, parsed)

	_, ok = ParseType("listenbrains")
	assert.False(t, ok)
}

// TestDefaultOrder_ListenBrainzPlacement verifies ListenBrainz runs after
// MusicBrainz (it needs MBIDs) and after Spotify (which stays the primary
// popularity source under REQ-ENRICH-020 first-writer-wins merging), and
// that OpenAI stays last.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-011, REQ-ENRICH-012,
// REQ "ListenBrainz Enricher" (REQ-ENRICH-064)
func TestDefaultOrder_ListenBrainzPlacement(t *testing.T) {
	order := DefaultOrder()

	idx := func(target Type) int {
		for i, ty := range order {
			if ty == target {
				return i
			}
		}
		return -1
	}

	lb := idx(TypeListenBrainz)
	require.NotEqual(t, -1, lb, "listenbrainz must be in the default order")
	assert.Greater(t, lb, idx(TypeMusicBrainz))
	assert.Greater(t, lb, idx(TypeSpotify))
	assert.Equal(t, TypeMusicBrainz, order[0])
	assert.Equal(t, TypeOpenAI, order[len(order)-1])
}
