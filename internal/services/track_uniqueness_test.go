// Governing: SPEC metadata-enrichment-pipeline (catalog uniqueness)
package services

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"spotter/ent"
	"spotter/ent/enttest"
	"spotter/internal/config"
	"spotter/internal/events"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTrackUniquenessTest(t *testing.T) (*ent.Client, *MetadataService) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewMetadataService(client, nil, &config.Config{}, logger, events.NewBus())
	return client, svc
}

// TestTrackUniqueIndex verifies the schema-level unique constraint on
// (artist, name) introduced for catalog uniqueness.
func TestTrackUniqueIndex(t *testing.T) {
	client, _ := setupTrackUniquenessTest(t)
	ctx := context.Background()

	u, err := client.User.Create().SetUsername("testuser").Save(ctx)
	require.NoError(t, err)
	art, err := client.Artist.Create().SetName("Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)

	_, err = client.Track.Create().SetName("Song").SetArtist(art).Save(ctx)
	require.NoError(t, err)

	// Same (artist, name) must be rejected by the unique index.
	_, err = client.Track.Create().SetName("Song").SetArtist(art).Save(ctx)
	require.Error(t, err)
	assert.True(t, ent.IsConstraintError(err), "duplicate (artist, name) should be a constraint error, got: %v", err)

	// Same name under a different artist is allowed.
	other, err := client.Artist.Create().SetName("Other Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)
	_, err = client.Track.Create().SetName("Song").SetArtist(other).Save(ctx)
	require.NoError(t, err)
}

// TestGetOrCreateTrack_ExistingWithDifferentAlbum verifies that getOrCreateTrack
// returns the existing (artist, name) track instead of attempting to create a
// duplicate when the album differs — the case that previously violated the
// unique index.
func TestGetOrCreateTrack_ExistingWithDifferentAlbum(t *testing.T) {
	client, svc := setupTrackUniquenessTest(t)
	ctx := context.Background()

	u, err := client.User.Create().SetUsername("testuser").Save(ctx)
	require.NoError(t, err)
	art, err := client.Artist.Create().SetName("Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)
	albumA, err := client.Album.Create().SetName("Album A").SetUser(u).SetArtist(art).Save(ctx)
	require.NoError(t, err)
	albumB, err := client.Album.Create().SetName("Album B").SetUser(u).SetArtist(art).Save(ctx)
	require.NoError(t, err)

	created, isNew, err := svc.getOrCreateTrack(ctx, art, albumA, "Song")
	require.NoError(t, err)
	assert.True(t, isNew, "first call should create the track")

	// Same (artist, name) with a different album must return the existing track.
	got, isNew, err := svc.getOrCreateTrack(ctx, art, albumB, "Song")
	require.NoError(t, err)
	assert.False(t, isNew, "second call must not create a duplicate")
	assert.Equal(t, created.ID, got.ID)

	// Same call with nil album also resolves to the existing track.
	got, isNew, err = svc.getOrCreateTrack(ctx, art, nil, "Song")
	require.NoError(t, err)
	assert.False(t, isNew)
	assert.Equal(t, created.ID, got.ID)

	count, err := client.Track.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "exactly one track should exist for the (artist, name) pair")
}

// TestMatchListens_AlbumMismatchFallsBack verifies that MatchListens links a
// listen to the unique (artist, name) track even when the listen's album name
// resolves to a different album than the track's.
func TestMatchListens_AlbumMismatchFallsBack(t *testing.T) {
	client, svc := setupTrackUniquenessTest(t)
	ctx := context.Background()

	u, err := client.User.Create().SetUsername("testuser").Save(ctx)
	require.NoError(t, err)
	art, err := client.Artist.Create().SetName("Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)
	albumA, err := client.Album.Create().SetName("Album A").SetUser(u).SetArtist(art).Save(ctx)
	require.NoError(t, err)
	albumB, err := client.Album.Create().SetName("Album B").SetUser(u).SetArtist(art).Save(ctx)
	require.NoError(t, err)

	trk, err := client.Track.Create().SetName("Song").SetArtist(art).SetAlbum(albumA).Save(ctx)
	require.NoError(t, err)
	_ = albumB

	// Listen reports the same track under Album B (e.g. a compilation).
	l, err := client.Listen.Create().
		SetUser(u).
		SetTrackName("Song").
		SetArtistName("Artist").
		SetAlbumName("Album B").
		SetSource("navidrome").
		SetPlayedAt(time.Now()).
		Save(ctx)
	require.NoError(t, err)

	matched, err := svc.MatchListens(ctx, u)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, matched, 1)

	linked, err := client.Listen.Query().QueryTrack().Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, trk.ID, linked.ID, "listen %d should link to the unique (artist, name) track", l.ID)
}
