// White-box tests for the transactional enhancement apply (issue #13):
// a failure while replacing playlist tracks must roll back, never leaving the
// playlist truncated (which auto-sync would then propagate to Navidrome).
package handlers

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"spotter/ent"
	"spotter/ent/enttest"
	"spotter/ent/playlist"
	"spotter/ent/playlisttrack"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupApplyTest(t *testing.T) (*ent.Client, *Handler, *ent.User, *ent.Playlist) {
	dbName := strings.NewReplacer("/", "_", " ", "_", "=", "_").Replace(t.Name())
	client := enttest.Open(t, "sqlite3", "file:"+dbName+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	h := &Handler{
		Client: client,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	ctx := context.Background()
	u, err := client.User.Create().
		SetUsername("applytx_user").
		SetPaginationSize(25).
		Save(ctx)
	require.NoError(t, err)

	pl, err := client.Playlist.Create().
		SetUser(u).
		SetRemoteID("apply-tx-playlist").
		SetName("Apply TX Playlist").
		SetSource("spotify").
		SetTrackCount(2).
		Save(ctx)
	require.NoError(t, err)

	// Seed the playlist with two existing tracks.
	for i, name := range []string{"Original One", "Original Two"} {
		_, err := client.PlaylistTrack.Create().
			SetPlaylist(pl).
			SetPosition(i + 1).
			SetTrackName(name).
			SetArtistName("Original Artist").
			Save(ctx)
		require.NoError(t, err)
	}

	return client, h, u, pl
}

func createApplyTestTrack(t *testing.T, client *ent.Client, u *ent.User, name string) *ent.Track {
	ctx := context.Background()
	a, err := client.Artist.Create().
		SetName("Artist of " + name).
		SetUser(u).
		Save(ctx)
	require.NoError(t, err)
	tr, err := client.Track.Create().
		SetName(name).
		SetArtist(a).
		Save(ctx)
	require.NoError(t, err)
	return tr
}

func TestApplyEnhancementTracks_Success(t *testing.T) {
	client, h, u, pl := setupApplyTest(t)
	ctx := context.Background()

	t1 := createApplyTestTrack(t, client, u, "New Track 1")
	t2 := createApplyTestTrack(t, client, u, "New Track 2")
	t3 := createApplyTestTrack(t, client, u, "New Track 3")

	err := h.applyEnhancementTracks(ctx, pl, []int{t1.ID, t2.ID, t3.ID})
	require.NoError(t, err)

	pts, err := client.PlaylistTrack.Query().
		Where(playlisttrack.HasPlaylistWith(playlist.ID(pl.ID))).
		Order(ent.Asc(playlisttrack.FieldPosition)).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, pts, 3)
	assert.Equal(t, "New Track 1", pts[0].TrackName)
	assert.Equal(t, "New Track 3", pts[2].TrackName)

	updated, err := client.Playlist.Get(ctx, pl.ID)
	require.NoError(t, err)
	assert.Equal(t, 3, updated.TrackCount)
}

func TestApplyEnhancementTracks_RollsBackOnFailure(t *testing.T) {
	client, h, u, pl := setupApplyTest(t)
	ctx := context.Background()

	good := createApplyTestTrack(t, client, u, "Good Track")

	// A track without an artist edge produces an empty artist_name, which
	// violates the PlaylistTrack NotEmpty validator mid-transaction.
	artistless, err := client.Track.Create().
		SetName("Artistless Track").
		Save(ctx)
	require.NoError(t, err)

	err = h.applyEnhancementTracks(ctx, pl, []int{good.ID, artistless.ID})
	require.Error(t, err, "insert failure must surface as an error")

	// The original playlist contents must be fully intact — no truncation,
	// no partial insert.
	pts, err := client.PlaylistTrack.Query().
		Where(playlisttrack.HasPlaylistWith(playlist.ID(pl.ID))).
		Order(ent.Asc(playlisttrack.FieldPosition)).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, pts, 2, "rollback must restore the original tracks")
	assert.Equal(t, "Original One", pts[0].TrackName)
	assert.Equal(t, "Original Two", pts[1].TrackName)

	updated, err := client.Playlist.Get(ctx, pl.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, updated.TrackCount, "track count must be unchanged after rollback")
}
