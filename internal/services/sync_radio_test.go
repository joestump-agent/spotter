// Governing: ADR-0030 (LB Radio through the standard playlist pipeline),
// SPEC music-provider-integration REQ "ListenBrainz Provider" (REQ-PROV-053)
package services_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"spotter/ent/enttest"
	"spotter/ent/playlist"
	"spotter/ent/playlisttrack"
	"spotter/internal/config"
	"spotter/internal/events"
	"spotter/internal/providers"
	"spotter/internal/providers/listenbrainz"
	"spotter/internal/services"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Governing: SPEC music-provider-integration REQ-PROV-053 (reconciler
// exemption) — a ListenBrainz playlist sync must NOT deactivate locally
// generated LB Radio playlists (the provider never returns them), while
// still deactivating imported playlists that vanished from the provider.
func TestReconcileInactivePlaylists_KeepsLBRadioPlaylistsActive(t *testing.T) {
	// ListenBrainz now returns zero playlists: any previously imported
	// playlist is stale, but the locally generated radio playlist is not.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"playlists": [], "playlist_count": 0, "count": 0, "offset": 0}`)
	}))
	defer server.Close()

	client := enttest.Open(t, "sqlite3", "file:ent_lb_radio_reconcile?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.ListenBrainz.APIURL = server.URL
	syncer := services.NewSyncer(client, cfg, logger, events.NewBus(), nil)

	ctx := context.Background()
	user := createTestUser(t, client)
	_, err := client.ListenBrainzAuth.Create().
		SetUser(user).
		SetUsername("lb_user").
		SetToken("test_token").
		Save(ctx)
	require.NoError(t, err)

	// Locally generated LB Radio playlist, persisted through the shared path.
	radio, err := syncer.UpsertGeneratedPlaylist(ctx, user, providers.TypeListenBrainz, providers.Playlist{
		ID:         listenbrainz.RadioRemoteID("tag:(jazz)"),
		Name:       "LB Radio: tag:(jazz)",
		TrackCount: 1,
		Tracks:     []providers.Track{{Name: "Song", Artist: "Artist"}},
	})
	require.NoError(t, err)
	require.True(t, radio.IsActive)

	// Previously imported ListenBrainz playlist that the server no longer returns.
	stale, err := client.Playlist.Create().
		SetUser(user).
		SetRemoteID("11111111-2222-3333-4444-555555555555").
		SetName("Old Weekly Jams").
		SetSource(string(providers.TypeListenBrainz)).
		Save(ctx)
	require.NoError(t, err)

	syncer.Register(listenbrainz.New(logger, cfg))
	require.NoError(t, syncer.SyncPlaylists(ctx, user))

	radioAfter, err := client.Playlist.Get(ctx, radio.ID)
	require.NoError(t, err)
	assert.True(t, radioAfter.IsActive, "lb-radio playlists must survive the missing-from-provider reconciler")

	staleAfter, err := client.Playlist.Get(ctx, stale.ID)
	require.NoError(t, err)
	assert.False(t, staleAfter.IsActive, "imported playlists missing from the provider are still deactivated")
}

// Governing: ADR-0030, SPEC music-provider-integration REQ-PROV-053 —
// UpsertGeneratedPlaylist reuses the syncer's persist path (upsert +
// persistPlaylistTracks), so regeneration preserves catalog links made by
// the metadata service's name/artist matching. Asserted via the persisted
// rows: no matcher code is duplicated for radio playlists.
func TestUpsertGeneratedPlaylist_RegenerationPreservesCatalogLinks(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent_lb_radio_upsert?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	syncer := services.NewSyncer(client, &config.Config{}, logger, events.NewBus(), nil)

	ctx := context.Background()
	user := createTestUser(t, client)

	const mbid = "2cfad207-3f55-4aec-8120-86cf66e34d59"
	gen := providers.Playlist{
		ID:         listenbrainz.RadioRemoteID("artist:(nina simone)"),
		Name:       "LB Radio: artist:(nina simone)",
		TrackCount: 1,
		Tracks:     []providers.Track{{ID: mbid, Name: "Feeling Good", Artist: "Nina Simone"}},
	}

	first, err := syncer.UpsertGeneratedPlaylist(ctx, user, providers.TypeListenBrainz, gen)
	require.NoError(t, err)

	// Simulate the metadata service's catalog link pass (name/artist match).
	artist, err := client.Artist.Create().SetUser(user).SetName("Nina Simone").Save(ctx)
	require.NoError(t, err)
	track, err := client.Track.Create().SetArtist(artist).SetName("Feeling Good").Save(ctx)
	require.NoError(t, err)
	pt, err := client.PlaylistTrack.Query().
		Where(playlisttrack.HasPlaylistWith(playlist.ID(first.ID))).
		Only(ctx)
	require.NoError(t, err)
	require.NoError(t, client.PlaylistTrack.UpdateOne(pt).SetTrack(track).SetArtist(artist).Exec(ctx))

	// Regenerate: same remote ID, same track (as the same recording MBID).
	second, err := syncer.UpsertGeneratedPlaylist(ctx, user, providers.TypeListenBrainz, gen)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID, "same remote ID upserts in place")

	rows, err := client.PlaylistTrack.Query().
		Where(playlisttrack.HasPlaylistWith(playlist.ID(first.ID))).
		WithTrack().
		All(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, pt.ID, rows[0].ID, "existing row updated by remote_id, not recreated")
	require.NotNil(t, rows[0].Edges.Track, "catalog link survives regeneration")
	assert.Equal(t, track.ID, rows[0].Edges.Track.ID)

	// Missing remote ID or name is rejected (the deterministic key is the contract).
	_, err = syncer.UpsertGeneratedPlaylist(ctx, user, providers.TypeListenBrainz, providers.Playlist{Name: "x"})
	assert.ErrorContains(t, err, "remote ID")
	_, err = syncer.UpsertGeneratedPlaylist(ctx, user, providers.TypeListenBrainz, providers.Playlist{ID: "lb-radio:x"})
	assert.ErrorContains(t, err, "name")
}

// Governing: ADR-0030, SPEC music-provider-integration REQ-PROV-053 —
// distinct LB Radio prompts MUST produce distinct playlists. The
// distinctness is derived from the prompt via RadioRemoteID (lb-radio:<prompt>),
// so two prompts that differ only by case must not collapse into one row.
// This locks the requirement directly: the sibling regeneration test proves
// the SAME prompt updates in place, and this proves DIFFERENT prompts do not.
// Without it, a future prompt-normalization refactor (e.g. lowercasing in
// RadioRemoteID) could silently merge case-variant prompts undetected — every
// other prompt in these tests is already lowercase.
func TestUpsertGeneratedPlaylist_DistinctPromptsCreateDistinctPlaylists(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent_lb_radio_distinct?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	syncer := services.NewSyncer(client, &config.Config{}, logger, events.NewBus(), nil)

	ctx := context.Background()
	user := createTestUser(t, client)

	// Case-variant prompts: identical apart from letter case. RadioRemoteID
	// trims but does not fold case, so these are genuinely different prompts.
	const (
		promptUpper = "tag:(Soul)"
		promptLower = "tag:(soul)"
	)
	require.NotEqual(t, listenbrainz.RadioRemoteID(promptUpper), listenbrainz.RadioRemoteID(promptLower),
		"case-variant prompts must derive distinct remote IDs")

	first, err := syncer.UpsertGeneratedPlaylist(ctx, user, providers.TypeListenBrainz, providers.Playlist{
		ID:         listenbrainz.RadioRemoteID(promptUpper),
		Name:       "LB Radio: " + promptUpper,
		TrackCount: 1,
		Tracks:     []providers.Track{{Name: "Cold Sweat", Artist: "James Brown"}},
	})
	require.NoError(t, err)

	second, err := syncer.UpsertGeneratedPlaylist(ctx, user, providers.TypeListenBrainz, providers.Playlist{
		ID:         listenbrainz.RadioRemoteID(promptLower),
		Name:       "LB Radio: " + promptLower,
		TrackCount: 1,
		Tracks:     []providers.Track{{Name: "Superstition", Artist: "Stevie Wonder"}},
	})
	require.NoError(t, err)

	assert.NotEqual(t, first.ID, second.ID, "distinct prompts must create distinct playlist rows")
	assert.NotEqual(t, first.RemoteID, second.RemoteID, "distinct prompts must persist distinct remote IDs")

	count, err := client.User.QueryPlaylists(user).
		Where(playlist.RemoteIDHasPrefix(providers.ListenBrainzRadioRemoteIDPrefix)).
		Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, count, "two distinct prompts must leave two lb-radio: rows for the user")
}
