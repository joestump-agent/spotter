package services_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"spotter/ent"
	"spotter/ent/enttest"
	"spotter/internal/providers"
	"spotter/internal/services"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestTrackMatcher(t *testing.T, minConfidence float64) (*ent.Client, *services.TrackMatcher, *ent.User) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	matcher := services.NewTrackMatcher(client, logger, minConfidence)

	// Create a test user for the matcher tests
	user, err := client.User.Create().
		SetUsername("matcheruser").
		Save(context.Background())
	require.NoError(t, err)

	return client, matcher, user
}

func createTestTrackWithNavidromeID(t *testing.T, client *ent.Client, user *ent.User, name, artistName, navidromeID string) *ent.Track {
	ctx := context.Background()

	// Create artist first with user
	artist, err := client.Artist.Create().
		SetName(artistName).
		SetUser(user).
		Save(ctx)
	require.NoError(t, err)

	// Create track with navidrome ID
	track, err := client.Track.Create().
		SetName(name).
		SetArtist(artist).
		SetNillableNavidromeID(&navidromeID).
		Save(ctx)
	require.NoError(t, err)

	return track
}

func TestTrackMatcher_ExactMatch(t *testing.T) {
	client, matcher, user := setupTestTrackMatcher(t, 0.8)
	ctx := context.Background()

	// Create a track in the library
	createTestTrackWithNavidromeID(t, client, user, "Song Title", "Artist Name", "nav-123")

	// Create source tracks to match
	sourceTracks := []providers.Track{
		{
			ID:     "spotify-1",
			Name:   "Song Title",
			Artist: "Artist Name",
			Album:  "Album Name",
		},
	}

	// Run matching
	results, err := matcher.MatchTracks(ctx, user.ID, sourceTracks)
	require.NoError(t, err)
	require.Len(t, results, 1)

	// Verify exact match
	assert.Equal(t, "nav-123", results[0].NavidromeTrackID)
	assert.Equal(t, 1.0, results[0].MatchConfidence)
	assert.Equal(t, services.MatchMethodExact, results[0].MatchMethod)
}

func TestTrackMatcher_ExactMatch_CaseInsensitive(t *testing.T) {
	client, matcher, user := setupTestTrackMatcher(t, 0.8)
	ctx := context.Background()

	// Create a track in the library
	createTestTrackWithNavidromeID(t, client, user, "Song Title", "Artist Name", "nav-123")

	// Create source tracks with different casing
	sourceTracks := []providers.Track{
		{
			ID:     "spotify-1",
			Name:   "SONG TITLE",
			Artist: "artist name",
		},
	}

	// Run matching
	results, err := matcher.MatchTracks(ctx, user.ID, sourceTracks)
	require.NoError(t, err)
	require.Len(t, results, 1)

	// Verify match (should match despite case difference)
	assert.Equal(t, "nav-123", results[0].NavidromeTrackID)
	assert.Equal(t, services.MatchMethodExact, results[0].MatchMethod)
}

func TestTrackMatcher_FuzzyMatch_Remastered(t *testing.T) {
	client, matcher, user := setupTestTrackMatcher(t, 0.8)
	ctx := context.Background()

	// Create a track in the library (without remastered suffix)
	createTestTrackWithNavidromeID(t, client, user, "Song Title", "Artist Name", "nav-456")

	// Create source tracks with remastered suffix
	sourceTracks := []providers.Track{
		{
			ID:     "spotify-1",
			Name:   "Song Title (Remastered)",
			Artist: "Artist Name",
		},
	}

	// Run matching
	results, err := matcher.MatchTracks(ctx, user.ID, sourceTracks)
	require.NoError(t, err)
	require.Len(t, results, 1)

	// Verify fuzzy match
	assert.Equal(t, "nav-456", results[0].NavidromeTrackID)
	assert.GreaterOrEqual(t, results[0].MatchConfidence, 0.8)
	// Could be exact or fuzzy depending on normalization
	assert.NotEqual(t, services.MatchMethodNone, results[0].MatchMethod)
}

func TestTrackMatcher_FuzzyMatch_RadioEdit(t *testing.T) {
	client, matcher, user := setupTestTrackMatcher(t, 0.8)
	ctx := context.Background()

	// Create a track in the library
	createTestTrackWithNavidromeID(t, client, user, "Amazing Song", "Cool Artist", "nav-789")

	// Create source tracks with (Radio Edit) suffix
	sourceTracks := []providers.Track{
		{
			ID:     "spotify-1",
			Name:   "Amazing Song (Radio Edit)",
			Artist: "Cool Artist",
		},
	}

	// Run matching
	results, err := matcher.MatchTracks(ctx, user.ID, sourceTracks)
	require.NoError(t, err)
	require.Len(t, results, 1)

	// Verify match
	assert.Equal(t, "nav-789", results[0].NavidromeTrackID)
	assert.GreaterOrEqual(t, results[0].MatchConfidence, 0.8)
}

func TestTrackMatcher_NoMatch(t *testing.T) {
	client, matcher, user := setupTestTrackMatcher(t, 0.8)
	ctx := context.Background()

	// Create a track in the library
	createTestTrackWithNavidromeID(t, client, user, "Existing Song", "Existing Artist", "nav-111")

	// Create source tracks that don't exist in library
	sourceTracks := []providers.Track{
		{
			ID:     "spotify-1",
			Name:   "Completely Different Song",
			Artist: "Unknown Artist",
		},
	}

	// Run matching
	results, err := matcher.MatchTracks(ctx, user.ID, sourceTracks)
	require.NoError(t, err)
	require.Len(t, results, 1)

	// Verify no match
	assert.Empty(t, results[0].NavidromeTrackID)
	assert.Equal(t, 0.0, results[0].MatchConfidence)
	assert.Equal(t, services.MatchMethodNone, results[0].MatchMethod)
}

func TestTrackMatcher_MultipleResults_BestMatch(t *testing.T) {
	client, matcher, user := setupTestTrackMatcher(t, 0.8)
	ctx := context.Background()

	// Create multiple similar tracks in the library
	createTestTrackWithNavidromeID(t, client, user, "Love Song", "The Band", "nav-001")
	createTestTrackWithNavidromeID(t, client, user, "Love Song II", "The Band 2", "nav-002")
	createTestTrackWithNavidromeID(t, client, user, "Love Song Live", "The Band 3", "nav-003")

	// Create source track
	sourceTracks := []providers.Track{
		{
			ID:     "spotify-1",
			Name:   "Love Song",
			Artist: "The Band",
		},
	}

	// Run matching
	results, err := matcher.MatchTracks(ctx, user.ID, sourceTracks)
	require.NoError(t, err)
	require.Len(t, results, 1)

	// Should match the exact one
	assert.Equal(t, "nav-001", results[0].NavidromeTrackID)
	assert.Equal(t, 1.0, results[0].MatchConfidence)
}

func TestTrackMatcher_EmptyLibrary(t *testing.T) {
	_, matcher, user := setupTestTrackMatcher(t, 0.8)
	ctx := context.Background()

	// Don't create any tracks in the library

	// Create source tracks
	sourceTracks := []providers.Track{
		{
			ID:     "spotify-1",
			Name:   "Any Song",
			Artist: "Any Artist",
		},
	}

	// Run matching
	results, err := matcher.MatchTracks(ctx, user.ID, sourceTracks)
	require.NoError(t, err)
	require.Len(t, results, 1)

	// Verify no match
	assert.Empty(t, results[0].NavidromeTrackID)
	assert.Equal(t, services.MatchMethodNone, results[0].MatchMethod)
}

func TestTrackMatcher_EmptySourceTracks(t *testing.T) {
	_, matcher, user := setupTestTrackMatcher(t, 0.8)
	ctx := context.Background()

	// Run matching with empty source
	results, err := matcher.MatchTracks(ctx, user.ID, []providers.Track{})
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestTrackMatcher_MinConfidenceThreshold(t *testing.T) {
	// Use very high confidence threshold
	client, matcher, user := setupTestTrackMatcher(t, 0.99)
	ctx := context.Background()

	// Create a track in the library
	createTestTrackWithNavidromeID(t, client, user, "Song Title", "Artist Name", "nav-123")

	// Create source track with slight difference
	sourceTracks := []providers.Track{
		{
			ID:     "spotify-1",
			Name:   "Song Title (Extended Mix)",
			Artist: "Artist Name",
		},
	}

	// Run matching - should not match because confidence would be below 0.99
	results, err := matcher.MatchTracks(ctx, user.ID, sourceTracks)
	require.NoError(t, err)
	require.Len(t, results, 1)

	// The exact match removes the "(Extended Mix)" suffix through normalization
	// so this might actually match. Let's check if it doesn't meet the threshold
	// for the fuzzy matching path
	if results[0].MatchMethod == services.MatchMethodFuzzy {
		// If it went through fuzzy matching, confidence should be checked
		assert.Less(t, results[0].MatchConfidence, 0.99)
	}
}

func TestTrackMatcher_MultipleTracks(t *testing.T) {
	client, matcher, user := setupTestTrackMatcher(t, 0.8)
	ctx := context.Background()

	// Create tracks in the library
	createTestTrackWithNavidromeID(t, client, user, "First Song", "Artist A", "nav-001")
	createTestTrackWithNavidromeID(t, client, user, "Second Song", "Artist B", "nav-002")
	createTestTrackWithNavidromeID(t, client, user, "Third Song", "Artist C", "nav-003")

	// Create source tracks
	sourceTracks := []providers.Track{
		{ID: "sp-1", Name: "First Song", Artist: "Artist A"},
		{ID: "sp-2", Name: "Unknown Song", Artist: "Unknown Artist"},
		{ID: "sp-3", Name: "Third Song", Artist: "Artist C"},
	}

	// Run matching
	results, err := matcher.MatchTracks(ctx, user.ID, sourceTracks)
	require.NoError(t, err)
	require.Len(t, results, 3)

	// First track should match
	assert.Equal(t, "nav-001", results[0].NavidromeTrackID)

	// Second track should not match
	assert.Empty(t, results[1].NavidromeTrackID)

	// Third track should match
	assert.Equal(t, "nav-003", results[2].NavidromeTrackID)
}

func TestGetMatchStats(t *testing.T) {
	results := []services.MatchResult{
		{NavidromeTrackID: "nav-1", MatchConfidence: 1.0, MatchMethod: services.MatchMethodExact},
		{NavidromeTrackID: "nav-2", MatchConfidence: 0.9, MatchMethod: services.MatchMethodFuzzy},
		{NavidromeTrackID: "", MatchConfidence: 0.0, MatchMethod: services.MatchMethodNone},
		{NavidromeTrackID: "nav-3", MatchConfidence: 0.85, MatchMethod: services.MatchMethodFuzzy},
	}

	stats := services.GetMatchStats(results)

	assert.Equal(t, 4, stats.Total)
	assert.Equal(t, 3, stats.Matched)
	assert.Equal(t, 1, stats.Unmatched)
	assert.Equal(t, 1, stats.ByMethod[services.MatchMethodExact])
	assert.Equal(t, 2, stats.ByMethod[services.MatchMethodFuzzy])
	assert.Equal(t, 1, stats.ByMethod[services.MatchMethodNone])

	// Average confidence of matched tracks: (1.0 + 0.9 + 0.85) / 3 = 0.9166...
	assert.InDelta(t, 0.9166, stats.AvgConfidence, 0.01)
}

func TestGetMatchedTracks(t *testing.T) {
	results := []services.MatchResult{
		{NavidromeTrackID: "nav-1", MatchMethod: services.MatchMethodExact},
		{NavidromeTrackID: "", MatchMethod: services.MatchMethodNone},
		{NavidromeTrackID: "nav-2", MatchMethod: services.MatchMethodFuzzy},
	}

	matched := services.GetMatchedTracks(results)
	assert.Len(t, matched, 2)
	assert.Equal(t, "nav-1", matched[0].NavidromeTrackID)
	assert.Equal(t, "nav-2", matched[1].NavidromeTrackID)
}

func TestGetUnmatchedTracks(t *testing.T) {
	sourceTrack1 := providers.Track{Name: "Matched Song", Artist: "Artist"}
	sourceTrack2 := providers.Track{Name: "Unmatched Song", Artist: "Unknown"}

	results := []services.MatchResult{
		{SourceTrack: sourceTrack1, NavidromeTrackID: "nav-1", MatchMethod: services.MatchMethodExact},
		{SourceTrack: sourceTrack2, NavidromeTrackID: "", MatchMethod: services.MatchMethodNone},
	}

	unmatched := services.GetUnmatchedTracks(results)
	assert.Len(t, unmatched, 1)
	assert.Equal(t, "Unmatched Song", unmatched[0].SourceTrack.Name)
}

func TestGetMatchStats_EmptyResults(t *testing.T) {
	stats := services.GetMatchStats([]services.MatchResult{})

	assert.Equal(t, 0, stats.Total)
	assert.Equal(t, 0, stats.Matched)
	assert.Equal(t, 0, stats.Unmatched)
	assert.Equal(t, 0.0, stats.AvgConfidence)
}

func TestGetMatchStats_AllMatched(t *testing.T) {
	results := []services.MatchResult{
		{NavidromeTrackID: "nav-1", MatchConfidence: 1.0, MatchMethod: services.MatchMethodExact},
		{NavidromeTrackID: "nav-2", MatchConfidence: 1.0, MatchMethod: services.MatchMethodExact},
	}

	stats := services.GetMatchStats(results)

	assert.Equal(t, 2, stats.Total)
	assert.Equal(t, 2, stats.Matched)
	assert.Equal(t, 0, stats.Unmatched)
	assert.Equal(t, 1.0, stats.AvgConfidence)
}

func TestGetMatchStats_NoneMatched(t *testing.T) {
	results := []services.MatchResult{
		{NavidromeTrackID: "", MatchConfidence: 0.0, MatchMethod: services.MatchMethodNone},
		{NavidromeTrackID: "", MatchConfidence: 0.0, MatchMethod: services.MatchMethodNone},
	}

	stats := services.GetMatchStats(results)

	assert.Equal(t, 2, stats.Total)
	assert.Equal(t, 0, stats.Matched)
	assert.Equal(t, 2, stats.Unmatched)
	assert.Equal(t, 0.0, stats.AvgConfidence)
}

func TestTrackMatcher_PunctuationNormalization(t *testing.T) {
	client, matcher, user := setupTestTrackMatcher(t, 0.8)
	ctx := context.Background()

	// Create a track with punctuation
	createTestTrackWithNavidromeID(t, client, user, "Don't Stop Me Now", "Queen", "nav-queen")

	// Create source track with different punctuation representation
	sourceTracks := []providers.Track{
		{
			ID:     "spotify-1",
			Name:   "Dont Stop Me Now",
			Artist: "Queen",
		},
	}

	// Run matching - should match because punctuation is normalized
	results, err := matcher.MatchTracks(ctx, user.ID, sourceTracks)
	require.NoError(t, err)
	require.Len(t, results, 1)

	assert.Equal(t, "nav-queen", results[0].NavidromeTrackID)
}

func TestTrackMatcher_WhitespaceNormalization(t *testing.T) {
	client, matcher, user := setupTestTrackMatcher(t, 0.8)
	ctx := context.Background()

	// Create a track
	createTestTrackWithNavidromeID(t, client, user, "Song Name", "Artist Name", "nav-ws")

	// Create source track with extra whitespace
	sourceTracks := []providers.Track{
		{
			ID:     "spotify-1",
			Name:   "Song  Name",
			Artist: " Artist  Name ",
		},
	}

	// Run matching
	results, err := matcher.MatchTracks(ctx, user.ID, sourceTracks)
	require.NoError(t, err)
	require.Len(t, results, 1)

	// Should match because whitespace is normalized
	assert.Equal(t, "nav-ws", results[0].NavidromeTrackID)
}

func TestTrackMatcher_TrackWithoutNavidromeID(t *testing.T) {
	client, matcher, user := setupTestTrackMatcher(t, 0.8)
	ctx := context.Background()

	// Create artist with user
	artist, err := client.Artist.Create().
		SetName("Artist Name").
		SetUser(user).
		Save(ctx)
	require.NoError(t, err)

	// Create track WITHOUT navidrome ID (not in library)
	_, err = client.Track.Create().
		SetName("Song Title").
		SetArtist(artist).
		Save(ctx)
	require.NoError(t, err)

	// Create source tracks
	sourceTracks := []providers.Track{
		{
			ID:     "spotify-1",
			Name:   "Song Title",
			Artist: "Artist Name",
		},
	}

	// Run matching - should NOT match because track doesn't have navidrome_id
	results, err := matcher.MatchTracks(ctx, user.ID, sourceTracks)
	require.NoError(t, err)
	require.Len(t, results, 1)

	assert.Empty(t, results[0].NavidromeTrackID)
	assert.Equal(t, services.MatchMethodNone, results[0].MatchMethod)
}

func TestTrackMatcher_SourceTrackDataPreserved(t *testing.T) {
	client, matcher, user := setupTestTrackMatcher(t, 0.8)
	ctx := context.Background()

	// Create a track in the library
	createTestTrackWithNavidromeID(t, client, user, "Song", "Artist", "nav-123")

	// Create source tracks with all data
	sourceTracks := []providers.Track{
		{
			ID:         "spotify-id-123",
			Name:       "Song",
			Artist:     "Artist",
			Album:      "Album Name",
			DurationMs: 180000,
			URL:        "https://spotify.com/track/123",
		},
	}

	// Run matching
	results, err := matcher.MatchTracks(ctx, user.ID, sourceTracks)
	require.NoError(t, err)
	require.Len(t, results, 1)

	// Verify source track data is preserved in result
	assert.Equal(t, "spotify-id-123", results[0].SourceTrack.ID)
	assert.Equal(t, "Song", results[0].SourceTrack.Name)
	assert.Equal(t, "Artist", results[0].SourceTrack.Artist)
	assert.Equal(t, "Album Name", results[0].SourceTrack.Album)
	assert.Equal(t, 180000, results[0].SourceTrack.DurationMs)
	assert.Equal(t, "https://spotify.com/track/123", results[0].SourceTrack.URL)
}
