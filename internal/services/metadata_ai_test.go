package services

import (
	"context"
	"testing"
	"time"

	"spotter/ent"
	"spotter/ent/album"
	"spotter/ent/artist"
	"spotter/ent/enttest"
	"spotter/ent/track"
	"spotter/ent/user"
	"spotter/internal/enrichers"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestArtistQueryIncludesAIEnrichmentCriteria verifies that artists needing AI enrichment
// are included in the enrichment query, even if they've been recently enriched by other enrichers.
// This is a regression test for a bug where artists with recent last_enriched_at were skipped
// even if they had never been AI enriched.
func TestArtistQueryIncludesAIEnrichmentCriteria(t *testing.T) {
	// Setup
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	defer client.Close()

	ctx := context.Background()

	// Create test user
	u, err := client.User.Create().
		SetUsername("testuser").
		SetTheme("dark").
		Save(ctx)
	require.NoError(t, err)

	// Create artist that was recently enriched but never AI enriched
	recentlyEnriched := time.Now().Add(-1 * time.Hour)
	artist1, err := client.Artist.Create().
		SetName("Artist With Recent Enrichment").
		SetUser(u).
		SetLastEnrichedAt(recentlyEnriched).
		// LastAiEnrichedAt is nil (never AI enriched)
		Save(ctx)
	require.NoError(t, err)

	// Create artist that was AI enriched long ago
	oldAIEnrichment := time.Now().Add(-10 * 24 * time.Hour) // 10 days ago
	artist2, err := client.Artist.Create().
		SetName("Artist With Old AI Enrichment").
		SetUser(u).
		SetLastEnrichedAt(recentlyEnriched).
		SetLastAiEnrichedAt(oldAIEnrichment).
		Save(ctx)
	require.NoError(t, err)

	// Create artist that was recently AI enriched (should NOT be included)
	recentAIEnrichment := time.Now().Add(-1 * time.Hour)
	_, err = client.Artist.Create().
		SetName("Artist With Recent AI Enrichment").
		SetUser(u).
		SetLastEnrichedAt(recentlyEnriched).
		SetLastAiEnrichedAt(recentAIEnrichment).
		Save(ctx)
	require.NoError(t, err)

	// Query artists needing enrichment using the same logic as EnrichArtists
	cutoff := time.Now().Add(-24 * time.Hour)
	aiCutoff := time.Now().Add(-7 * 24 * time.Hour)

	artists, err := client.Artist.Query().
		Where(
			artist.HasUserWith(user.ID(u.ID)),
			artist.Or(
				artist.LastEnrichedAtIsNil(),
				artist.LastEnrichedAtLT(cutoff),
				artist.LastAiEnrichedAtIsNil(),
				artist.LastAiEnrichedAtLT(aiCutoff),
			),
		).
		All(ctx)
	require.NoError(t, err)

	// Verify results
	assert.Len(t, artists, 2, "Should find 2 artists needing enrichment")

	artistNames := make(map[string]bool)
	for _, a := range artists {
		artistNames[a.Name] = true
	}

	assert.True(t, artistNames["Artist With Recent Enrichment"],
		"Artist with nil LastAiEnrichedAt should be included")
	assert.True(t, artistNames["Artist With Old AI Enrichment"],
		"Artist with old LastAiEnrichedAt should be included")
	assert.False(t, artistNames["Artist With Recent AI Enrichment"],
		"Artist with recent AI enrichment should NOT be included")

	// Verify artist1 specifically - it has recent enrichment but needs AI
	found := false
	for _, a := range artists {
		if a.ID == artist1.ID {
			found = true
			break
		}
	}
	assert.True(t, found, "Artist with recent last_enriched_at but nil last_ai_enriched_at should be included")

	// Verify artist2 specifically - it has old AI enrichment
	found = false
	for _, a := range artists {
		if a.ID == artist2.ID {
			found = true
			break
		}
	}
	assert.True(t, found, "Artist with old last_ai_enriched_at should be included")
}

// TestArtistQueryLoadsEdgesForAIEnrichment verifies that the artist query loads
// the necessary edges (albums, tracks, images) that the AI enricher needs.
// This is a regression test for a bug where edges weren't being loaded.
func TestArtistQueryLoadsEdgesForAIEnrichment(t *testing.T) {
	// Setup
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	defer client.Close()

	ctx := context.Background()

	// Create test user
	u, err := client.User.Create().
		SetUsername("testuser").
		SetTheme("dark").
		Save(ctx)
	require.NoError(t, err)

	// Create artist
	art, err := client.Artist.Create().
		SetName("Test Artist").
		SetUser(u).
		Save(ctx)
	require.NoError(t, err)

	// Create album for the artist
	alb, err := client.Album.Create().
		SetName("Test Album").
		SetUser(u).
		SetArtist(art).
		SetYear(2023).
		Save(ctx)
	require.NoError(t, err)

	// Create track for the artist and album
	_, err = client.Track.Create().
		SetName("Test Track").
		SetArtist(art).
		SetAlbum(alb).
		Save(ctx)
	require.NoError(t, err)

	// Create artist image
	_, err = client.ArtistImage.Create().
		SetArtist(art).
		SetURL("https://example.com/image.jpg").
		SetSource("test").
		SetImageType("thumbnail").
		Save(ctx)
	require.NoError(t, err)

	// Query artists with edges loaded (as the metadata service should)
	artists, err := client.Artist.Query().
		Where(artist.HasUserWith(user.ID(u.ID))).
		WithAlbums().
		WithTracks(func(q *ent.TrackQuery) {
			q.WithAlbum()
		}).
		WithImages().
		All(ctx)
	require.NoError(t, err)
	require.Len(t, artists, 1)

	loadedArtist := artists[0]

	// Verify edges are loaded
	assert.NotNil(t, loadedArtist.Edges.Albums, "Albums edge should be loaded")
	assert.Len(t, loadedArtist.Edges.Albums, 1, "Should have 1 album")
	assert.Equal(t, "Test Album", loadedArtist.Edges.Albums[0].Name)

	assert.NotNil(t, loadedArtist.Edges.Tracks, "Tracks edge should be loaded")
	assert.Len(t, loadedArtist.Edges.Tracks, 1, "Should have 1 track")
	assert.Equal(t, "Test Track", loadedArtist.Edges.Tracks[0].Name)

	// Verify track's album edge is also loaded
	assert.NotNil(t, loadedArtist.Edges.Tracks[0].Edges.Album, "Track's album edge should be loaded")
	assert.Equal(t, "Test Album", loadedArtist.Edges.Tracks[0].Edges.Album.Name)

	assert.NotNil(t, loadedArtist.Edges.Images, "Images edge should be loaded")
	assert.Len(t, loadedArtist.Edges.Images, 1, "Should have 1 image")
}

// TestAlbumQueryIncludesAIEnrichmentCriteria verifies albums needing AI enrichment
// are included in the query.
func TestAlbumQueryIncludesAIEnrichmentCriteria(t *testing.T) {
	// Setup
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	defer client.Close()

	ctx := context.Background()

	// Create test user
	u, err := client.User.Create().
		SetUsername("testuser").
		SetTheme("dark").
		Save(ctx)
	require.NoError(t, err)

	// Create artist
	art, err := client.Artist.Create().
		SetName("Test Artist").
		SetUser(u).
		Save(ctx)
	require.NoError(t, err)

	// Create album that was recently enriched but never AI enriched
	recentlyEnriched := time.Now().Add(-1 * time.Hour)
	album1, err := client.Album.Create().
		SetName("Album With Recent Enrichment").
		SetUser(u).
		SetArtist(art).
		SetLastEnrichedAt(recentlyEnriched).
		// LastAiEnrichedAt is nil
		Save(ctx)
	require.NoError(t, err)

	// Create album with old AI enrichment
	oldAIEnrichment := time.Now().Add(-10 * 24 * time.Hour)
	album2, err := client.Album.Create().
		SetName("Album With Old AI Enrichment").
		SetUser(u).
		SetArtist(art).
		SetLastEnrichedAt(recentlyEnriched).
		SetLastAiEnrichedAt(oldAIEnrichment).
		Save(ctx)
	require.NoError(t, err)

	// Create album with recent AI enrichment (should NOT be included)
	recentAIEnrichment := time.Now().Add(-1 * time.Hour)
	_, err = client.Album.Create().
		SetName("Album With Recent AI Enrichment").
		SetUser(u).
		SetArtist(art).
		SetLastEnrichedAt(recentlyEnriched).
		SetLastAiEnrichedAt(recentAIEnrichment).
		Save(ctx)
	require.NoError(t, err)

	// Query albums using the same logic as EnrichAlbums
	cutoff := time.Now().Add(-24 * time.Hour)
	aiCutoff := time.Now().Add(-7 * 24 * time.Hour)

	albums, err := client.Album.Query().
		Where(
			album.HasUserWith(user.ID(u.ID)),
			album.Or(
				album.LastEnrichedAtIsNil(),
				album.LastEnrichedAtLT(cutoff),
				album.LastAiEnrichedAtIsNil(),
				album.LastAiEnrichedAtLT(aiCutoff),
			),
		).
		All(ctx)
	require.NoError(t, err)

	assert.Len(t, albums, 2, "Should find 2 albums needing enrichment")

	albumIDs := make(map[int]bool)
	for _, a := range albums {
		albumIDs[a.ID] = true
	}

	assert.True(t, albumIDs[album1.ID], "Album with nil LastAiEnrichedAt should be included")
	assert.True(t, albumIDs[album2.ID], "Album with old LastAiEnrichedAt should be included")
}

// TestAlbumQueryLoadsEdgesForAIEnrichment verifies that album query loads necessary edges.
func TestAlbumQueryLoadsEdgesForAIEnrichment(t *testing.T) {
	// Setup
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	defer client.Close()

	ctx := context.Background()

	// Create test user
	u, err := client.User.Create().
		SetUsername("testuser").
		SetTheme("dark").
		Save(ctx)
	require.NoError(t, err)

	// Create artist
	art, err := client.Artist.Create().
		SetName("Test Artist").
		SetUser(u).
		SetBio("Artist biography").
		SetGenres([]string{"rock", "indie"}).
		Save(ctx)
	require.NoError(t, err)

	// Create album
	alb, err := client.Album.Create().
		SetName("Test Album").
		SetUser(u).
		SetArtist(art).
		Save(ctx)
	require.NoError(t, err)

	// Create track
	_, err = client.Track.Create().
		SetName("Test Track").
		SetArtist(art).
		SetAlbum(alb).
		Save(ctx)
	require.NoError(t, err)

	// Create album image
	_, err = client.AlbumImage.Create().
		SetAlbum(alb).
		SetURL("https://example.com/cover.jpg").
		SetSource("test").
		SetImageType("cover_front").
		Save(ctx)
	require.NoError(t, err)

	// Query albums with edges loaded
	albums, err := client.Album.Query().
		Where(album.HasUserWith(user.ID(u.ID))).
		WithArtist().
		WithTracks().
		WithImages().
		All(ctx)
	require.NoError(t, err)
	require.Len(t, albums, 1)

	loadedAlbum := albums[0]

	// Verify edges are loaded
	assert.NotNil(t, loadedAlbum.Edges.Artist, "Artist edge should be loaded")
	assert.Equal(t, "Test Artist", loadedAlbum.Edges.Artist.Name)
	assert.Equal(t, "Artist biography", loadedAlbum.Edges.Artist.Bio)
	assert.Equal(t, []string{"rock", "indie"}, loadedAlbum.Edges.Artist.Genres)

	assert.NotNil(t, loadedAlbum.Edges.Tracks, "Tracks edge should be loaded")
	assert.Len(t, loadedAlbum.Edges.Tracks, 1)

	assert.NotNil(t, loadedAlbum.Edges.Images, "Images edge should be loaded")
	assert.Len(t, loadedAlbum.Edges.Images, 1)
}

// TestTrackQueryIncludesAIEnrichmentCriteria verifies tracks needing AI enrichment
// are included in the query.
func TestTrackQueryIncludesAIEnrichmentCriteria(t *testing.T) {
	// Setup
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	defer client.Close()

	ctx := context.Background()

	// Create test user
	u, err := client.User.Create().
		SetUsername("testuser").
		SetTheme("dark").
		Save(ctx)
	require.NoError(t, err)

	// Create artist
	art, err := client.Artist.Create().
		SetName("Test Artist").
		SetUser(u).
		Save(ctx)
	require.NoError(t, err)

	// Create tracks with different enrichment states
	recentlyEnriched := time.Now().Add(-1 * time.Hour)

	track1, err := client.Track.Create().
		SetName("Track With Recent Enrichment").
		SetArtist(art).
		SetLastEnrichedAt(recentlyEnriched).
		// LastAiEnrichedAt is nil
		Save(ctx)
	require.NoError(t, err)

	oldAIEnrichment := time.Now().Add(-10 * 24 * time.Hour)
	track2, err := client.Track.Create().
		SetName("Track With Old AI Enrichment").
		SetArtist(art).
		SetLastEnrichedAt(recentlyEnriched).
		SetLastAiEnrichedAt(oldAIEnrichment).
		Save(ctx)
	require.NoError(t, err)

	recentAIEnrichment := time.Now().Add(-1 * time.Hour)
	_, err = client.Track.Create().
		SetName("Track With Recent AI Enrichment").
		SetArtist(art).
		SetLastEnrichedAt(recentlyEnriched).
		SetLastAiEnrichedAt(recentAIEnrichment).
		Save(ctx)
	require.NoError(t, err)

	// Query tracks using the same logic as EnrichTracks
	cutoff := time.Now().Add(-24 * time.Hour)
	aiCutoff := time.Now().Add(-7 * 24 * time.Hour)

	tracks, err := client.Track.Query().
		Where(
			track.Or(
				track.LastEnrichedAtIsNil(),
				track.LastEnrichedAtLT(cutoff),
				track.LastAiEnrichedAtIsNil(),
				track.LastAiEnrichedAtLT(aiCutoff),
			),
		).
		WithArtist(func(q *ent.ArtistQuery) {
			q.Where(artist.HasUserWith(user.ID(u.ID)))
		}).
		All(ctx)
	require.NoError(t, err)

	// Filter to tracks belonging to user's artists
	var userTracks []*ent.Track
	for _, tr := range tracks {
		if tr.Edges.Artist != nil {
			userTracks = append(userTracks, tr)
		}
	}

	assert.Len(t, userTracks, 2, "Should find 2 tracks needing enrichment")

	trackIDs := make(map[int]bool)
	for _, tr := range userTracks {
		trackIDs[tr.ID] = true
	}

	assert.True(t, trackIDs[track1.ID], "Track with nil LastAiEnrichedAt should be included")
	assert.True(t, trackIDs[track2.ID], "Track with old LastAiEnrichedAt should be included")
}


// TestAIEnrichmentFieldsAreSaved verifies that AI enrichment data is properly saved to the database.
func TestAIEnrichmentFieldsAreSaved(t *testing.T) {
	// Setup
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	defer client.Close()

	ctx := context.Background()

	// Create test user
	u, err := client.User.Create().
		SetUsername("testuser").
		SetTheme("dark").
		Save(ctx)
	require.NoError(t, err)

	// Create artist
	art, err := client.Artist.Create().
		SetName("Test Artist").
		SetUser(u).
		Save(ctx)
	require.NoError(t, err)

	// Simulate saving AI enrichment data
	now := time.Now()
	updatedArtist, err := client.Artist.UpdateOne(art).
		SetAiSummary("AI generated summary").
		SetAiBiography("AI generated biography").
		SetAiTags([]string{"tag1", "tag2", "tag3"}).
		SetLastAiEnrichedAt(now).
		Save(ctx)
	require.NoError(t, err)

	// Verify data was saved
	assert.Equal(t, "AI generated summary", updatedArtist.AiSummary)
	assert.Equal(t, "AI generated biography", updatedArtist.AiBiography)
	assert.Equal(t, []string{"tag1", "tag2", "tag3"}, updatedArtist.AiTags)
	assert.NotNil(t, updatedArtist.LastAiEnrichedAt)

	// Reload from database to ensure persistence
	reloadedArtist, err := client.Artist.Get(ctx, art.ID)
	require.NoError(t, err)

	assert.Equal(t, "AI generated summary", reloadedArtist.AiSummary)
	assert.Equal(t, "AI generated biography", reloadedArtist.AiBiography)
	assert.Equal(t, []string{"tag1", "tag2", "tag3"}, reloadedArtist.AiTags)
}

// TestAlbumAIEnrichmentFieldsAreSaved verifies album AI enrichment data is saved.
func TestAlbumAIEnrichmentFieldsAreSaved(t *testing.T) {
	// Setup
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	defer client.Close()

	ctx := context.Background()

	// Create test user
	u, err := client.User.Create().
		SetUsername("testuser").
		SetTheme("dark").
		Save(ctx)
	require.NoError(t, err)

	// Create album
	alb, err := client.Album.Create().
		SetName("Test Album").
		SetUser(u).
		Save(ctx)
	require.NoError(t, err)

	// Simulate saving AI enrichment data
	now := time.Now()
	updatedAlbum, err := client.Album.UpdateOne(alb).
		SetAiSummary("AI album summary with artist thoughts").
		SetAiTags([]string{"atmospheric", "introspective"}).
		SetLastAiEnrichedAt(now).
		Save(ctx)
	require.NoError(t, err)

	assert.Equal(t, "AI album summary with artist thoughts", updatedAlbum.AiSummary)
	assert.Equal(t, []string{"atmospheric", "introspective"}, updatedAlbum.AiTags)
	assert.NotNil(t, updatedAlbum.LastAiEnrichedAt)
}

// TestTrackAIEnrichmentFieldsAreSaved verifies track AI enrichment data is saved.
func TestTrackAIEnrichmentFieldsAreSaved(t *testing.T) {
	// Setup
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	defer client.Close()

	ctx := context.Background()

	// Create track
	tr, err := client.Track.Create().
		SetName("Test Track").
		Save(ctx)
	require.NoError(t, err)

	// Simulate saving AI enrichment data
	now := time.Now()
	updatedTrack, err := client.Track.UpdateOne(tr).
		SetAiSummary("AI track summary").
		SetAiTags([]string{"energetic", "uplifting"}).
		SetLastAiEnrichedAt(now).
		Save(ctx)
	require.NoError(t, err)

	assert.Equal(t, "AI track summary", updatedTrack.AiSummary)
	assert.Equal(t, []string{"energetic", "uplifting"}, updatedTrack.AiTags)
	assert.NotNil(t, updatedTrack.LastAiEnrichedAt)
}
