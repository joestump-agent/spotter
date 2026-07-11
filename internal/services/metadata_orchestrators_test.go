package services

// Tests for the MetadataService orchestrators: the selection queries and
// batching in EnrichArtists/EnrichAlbums/EnrichTracks, catalog building
// (BuildCatalog / processListenEntry / linkPlaylistTracks), EnrichNewListens,
// and the SyncAll shell. Enrichers are stubbed via the registry so no network
// is touched.
//
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-010/013/020/040/050,
// AGENTS.md SRV-AI-001..SRV-AI-006 (AI enrichment selection independence)

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"spotter/ent"
	"spotter/ent/album"
	"spotter/ent/artist"
	"spotter/ent/enttest"
	"spotter/ent/playlisttrack"
	"spotter/ent/predicate"
	"spotter/ent/syncevent"
	"spotter/ent/track"
	"spotter/internal/config"
	"spotter/internal/enrichers"
	"spotter/internal/events"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newOrchestratorTestService builds a MetadataService with a real registry and
// event bus over an in-memory DB, matching how NewMetadataService wires it in
// production. Metadata enrichment is enabled by default.
func newOrchestratorTestService(t *testing.T) *MetadataService {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	cfg := &config.Config{}
	cfg.Metadata.Enabled = true

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewMetadataService(client, nil, cfg, logger, events.NewBus())
}

func createTestUser(t *testing.T, svc *MetadataService, username string) *ent.User {
	t.Helper()
	u, err := svc.client.User.Create().SetUsername(username).SetTheme("dark").Save(context.Background())
	require.NoError(t, err)
	return u
}

// stubFullEnricher implements ArtistEnricher, AlbumEnricher, and TrackEnricher
// with configurable data/errors and records which entities it was asked to
// enrich. Orchestrator passes are sequential, so no locking is needed.
type stubFullEnricher struct {
	typ         enrichers.Type
	unavailable bool

	artistData *enrichers.ArtistData
	albumData  *enrichers.AlbumData
	trackData  *enrichers.TrackData

	artistErr error
	albumErr  error
	trackErr  error

	artistImages    []enrichers.ImageData
	albumImages     []enrichers.ImageData
	artistImagesErr error
	albumImagesErr  error
	// imagesOnlyFor, when set, restricts artist images to the named artist.
	imagesOnlyFor string

	artistsSeen []string
	albumsSeen  []string
	tracksSeen  []string
}

func (s *stubFullEnricher) Type() enrichers.Type { return s.typ }
func (s *stubFullEnricher) Name() string         { return string(s.typ) + "-stub" }
func (s *stubFullEnricher) IsAvailable() bool    { return !s.unavailable }

func (s *stubFullEnricher) EnrichArtist(_ context.Context, art *ent.Artist) (*enrichers.ArtistData, error) {
	s.artistsSeen = append(s.artistsSeen, art.Name)
	if s.artistErr != nil {
		return nil, s.artistErr
	}
	return s.artistData, nil
}

func (s *stubFullEnricher) GetArtistImages(_ context.Context, art *ent.Artist) ([]enrichers.ImageData, error) {
	if s.artistImagesErr != nil {
		return nil, s.artistImagesErr
	}
	if s.imagesOnlyFor != "" && art.Name != s.imagesOnlyFor {
		return nil, nil
	}
	return s.artistImages, nil
}

func (s *stubFullEnricher) EnrichAlbum(_ context.Context, alb *ent.Album) (*enrichers.AlbumData, error) {
	s.albumsSeen = append(s.albumsSeen, alb.Name)
	if s.albumErr != nil {
		return nil, s.albumErr
	}
	return s.albumData, nil
}

func (s *stubFullEnricher) GetAlbumImages(_ context.Context, _ *ent.Album) ([]enrichers.ImageData, error) {
	if s.albumImagesErr != nil {
		return nil, s.albumImagesErr
	}
	return s.albumImages, nil
}

func (s *stubFullEnricher) EnrichTrack(_ context.Context, tr *ent.Track) (*enrichers.TrackData, error) {
	s.tracksSeen = append(s.tracksSeen, tr.Name)
	if s.trackErr != nil {
		return nil, s.trackErr
	}
	return s.trackData, nil
}

// registerStub registers the stub in the service registry under its type.
func registerStub(t *testing.T, svc *MetadataService, stub *stubFullEnricher) {
	t.Helper()
	require.NoError(t, svc.Register(stub.typ, func(_ context.Context, _ *ent.User) (enrichers.Enricher, error) {
		return stub, nil
	}))
}

// Name-based predicate helpers keep test assertions terse.
func artistByName(name string) predicate.Artist { return artist.Name(name) }
func albumByName(name string) predicate.Album   { return album.Name(name) }
func trackByName(name string) predicate.Track   { return track.Name(name) }

// syncEventCount returns how many sync events of the given type exist.
func syncEventCount(t *testing.T, svc *MetadataService, et syncevent.EventType) int {
	t.Helper()
	n, err := svc.client.SyncEvent.Query().Where(syncevent.EventTypeEQ(et)).Count(context.Background())
	require.NoError(t, err)
	return n
}

// --- getActiveEnrichers ------------------------------------------------------

// TestGetActiveEnrichers_SkipsUnusableEntries exercises every skip arm of
// getActiveEnrichers: unknown type in order, unregistered type, factory error,
// factory nil return, and IsAvailable() == false. Only the healthy enricher
// must be activated, in config order.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-010, ADR-0015
func TestGetActiveEnrichers_SkipsUnusableEntries(t *testing.T) {
	svc := newOrchestratorTestService(t)
	svc.config.Metadata.Order = "bogus,lastfm,fanart,spotify,openai,musicbrainz"
	u := createTestUser(t, svc, "testuser")

	// lastfm: factory errors.
	require.NoError(t, svc.Register(enrichers.TypeLastFM, func(_ context.Context, _ *ent.User) (enrichers.Enricher, error) {
		return nil, fmt.Errorf("boom")
	}))
	// fanart: factory returns nil (not configured for this user).
	require.NoError(t, svc.Register(enrichers.TypeFanart, func(_ context.Context, _ *ent.User) (enrichers.Enricher, error) {
		return nil, nil
	}))
	// spotify: instantiated but reports unavailable.
	registerStub(t, svc, &stubFullEnricher{typ: enrichers.TypeSpotify, unavailable: true})
	// openai: never registered.
	// musicbrainz: healthy.
	healthy := &stubFullEnricher{typ: enrichers.TypeMusicBrainz}
	registerStub(t, svc, healthy)

	active, err := svc.getActiveEnrichers(context.Background(), u)
	require.NoError(t, err)
	require.Len(t, active, 1, "only the healthy enricher should be active")
	assert.Equal(t, enrichers.TypeMusicBrainz, active[0].Type())
}

// --- EnrichArtists ------------------------------------------------------------

// TestEnrichArtists_SelectionArms pins exactly which artists the selection
// query picks up: fresh (never enriched), stale regular enrichment, never
// AI-enriched, stale AI enrichment, and missing Lidarr ID each qualify;
// a fully enriched artist and another user's artist must be skipped.
// This is the selection that regressed in spotter-6p7, so each arm is pinned.
// Governing: AGENTS.md SRV-AI-001/SRV-AI-002/SRV-AI-004
func TestEnrichArtists_SelectionArms(t *testing.T) {
	svc := newOrchestratorTestService(t)
	u := createTestUser(t, svc, "testuser")
	other := createTestUser(t, svc, "otheruser")
	ctx := context.Background()

	recent := time.Now().Add(-1 * time.Hour)
	staleRegular := time.Now().Add(-48 * time.Hour)
	staleAI := time.Now().Add(-10 * 24 * time.Hour)

	mk := func(owner *ent.User, name string, apply func(*ent.ArtistCreate) *ent.ArtistCreate) {
		c := svc.client.Artist.Create().SetName(name).SetUser(owner)
		if apply != nil {
			c = apply(c)
		}
		_, err := c.Save(ctx)
		require.NoError(t, err)
	}

	mk(u, "fresh", nil)
	mk(u, "stale-regular", func(c *ent.ArtistCreate) *ent.ArtistCreate {
		return c.SetLastEnrichedAt(staleRegular).SetLastAiEnrichedAt(recent).SetLidarrID("101")
	})
	mk(u, "ai-never", func(c *ent.ArtistCreate) *ent.ArtistCreate {
		return c.SetLastEnrichedAt(recent).SetLidarrID("102")
	})
	mk(u, "ai-stale", func(c *ent.ArtistCreate) *ent.ArtistCreate {
		return c.SetLastEnrichedAt(recent).SetLastAiEnrichedAt(staleAI).SetLidarrID("103")
	})
	mk(u, "lidarr-missing", func(c *ent.ArtistCreate) *ent.ArtistCreate {
		return c.SetLastEnrichedAt(recent).SetLastAiEnrichedAt(recent)
	})
	mk(u, "fully-enriched", func(c *ent.ArtistCreate) *ent.ArtistCreate {
		return c.SetLastEnrichedAt(recent).SetLastAiEnrichedAt(recent).SetLidarrID("104")
	})
	mk(other, "other-user-fresh", nil)

	stub := &stubFullEnricher{
		typ:        enrichers.TypeMusicBrainz,
		artistData: &enrichers.ArtistData{Bio: "a bio"},
	}
	registerStub(t, svc, stub)

	count, err := svc.EnrichArtists(ctx, u)
	require.NoError(t, err)
	assert.Equal(t, 5, count, "five artists qualify for enrichment")

	assert.ElementsMatch(t,
		[]string{"fresh", "stale-regular", "ai-never", "ai-stale", "lidarr-missing"},
		stub.artistsSeen,
		"selection must include fresh, stale-regular, ai-never, ai-stale, and lidarr-missing arms only")

	// The skipped artist must be untouched.
	skipped, err := svc.client.Artist.Query().Where(artistByName("fully-enriched")).Only(ctx)
	require.NoError(t, err)
	assert.Empty(t, skipped.Bio, "fully enriched artist must not be re-enriched")

	// Enriched artists get their timestamp bumped and enrichment applied.
	fresh, err := svc.client.Artist.Query().Where(artistByName("fresh")).Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, "a bio", fresh.Bio)
	require.NotNil(t, fresh.LastEnrichedAt)
	assert.WithinDuration(t, time.Now(), *fresh.LastEnrichedAt, time.Minute)

	// One artist_enriched event per enriched artist.
	assert.Equal(t, 5, syncEventCount(t, svc, syncevent.EventTypeArtistEnriched))
}

// TestEnrichArtists_AISummaryOnlyDropsOutOfSelection pins the spotter-6p7 fix
// at the orchestrator level: an enrichment that sets only AISummary (no
// AITags) must still stamp LastAiEnrichedAt so the artist does not get
// re-selected on the next pass.
func TestEnrichArtists_AISummaryOnlyDropsOutOfSelection(t *testing.T) {
	svc := newOrchestratorTestService(t)
	u := createTestUser(t, svc, "testuser")
	ctx := context.Background()

	_, err := svc.client.Artist.Create().SetName("The Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)

	stub := &stubFullEnricher{
		typ: enrichers.TypeOpenAI,
		artistData: &enrichers.ArtistData{
			AISummary: "an AI summary",
			LidarrID:  "42", // satisfies the LidarrIDIsNil selection arm
		},
	}
	registerStub(t, svc, stub)

	count, err := svc.EnrichArtists(ctx, u)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, []string{"The Artist"}, stub.artistsSeen)

	art, err := svc.client.Artist.Query().Where(artistByName("The Artist")).Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, "an AI summary", art.AiSummary)
	require.NotNil(t, art.LastAiEnrichedAt, "summary-only AI enrichment must stamp LastAiEnrichedAt (spotter-6p7)")

	// Second pass: the artist is fully stamped and must not be selected again.
	count, err = svc.EnrichArtists(ctx, u)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "stamped artist must drop out of the selection query")
	assert.Len(t, stub.artistsSeen, 1, "enricher must not be invoked again for the stamped artist")
}

// TestEnrichArtists_EnricherErrorDoesNotAbortPass verifies that a failing
// enricher is logged and skipped while later enrichers still run and the
// artist still gets its LastEnrichedAt stamp.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-013
func TestEnrichArtists_EnricherErrorDoesNotAbortPass(t *testing.T) {
	svc := newOrchestratorTestService(t)
	u := createTestUser(t, svc, "testuser")
	ctx := context.Background()

	_, err := svc.client.Artist.Create().SetName("Artist A").SetUser(u).Save(ctx)
	require.NoError(t, err)
	_, err = svc.client.Artist.Create().SetName("Artist B").SetUser(u).Save(ctx)
	require.NoError(t, err)

	failing := &stubFullEnricher{typ: enrichers.TypeMusicBrainz, artistErr: fmt.Errorf("upstream down")}
	succeeding := &stubFullEnricher{typ: enrichers.TypeSpotify, artistData: &enrichers.ArtistData{Bio: "from spotify"}}
	registerStub(t, svc, failing)
	registerStub(t, svc, succeeding)

	count, err := svc.EnrichArtists(ctx, u)
	require.NoError(t, err)
	assert.Equal(t, 2, count, "both artists complete the pass despite the failing enricher")

	assert.ElementsMatch(t, []string{"Artist A", "Artist B"}, failing.artistsSeen)
	assert.ElementsMatch(t, []string{"Artist A", "Artist B"}, succeeding.artistsSeen,
		"later enrichers must still run after an earlier enricher fails")

	got, err := svc.client.Artist.Query().Where(artistByName("Artist A")).Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, "from spotify", got.Bio, "partial results from healthy enrichers must be preserved")
	assert.NotNil(t, got.LastEnrichedAt)
}

// TestEnrichArtists_BatchLimit pins the batch size of the selection query:
// only 100 artists are processed per pass.
func TestEnrichArtists_BatchLimit(t *testing.T) {
	svc := newOrchestratorTestService(t)
	u := createTestUser(t, svc, "testuser")
	ctx := context.Background()

	bulk := make([]*ent.ArtistCreate, 105)
	for i := range bulk {
		bulk[i] = svc.client.Artist.Create().SetName(fmt.Sprintf("artist-%03d", i)).SetUser(u)
	}
	_, err := svc.client.Artist.CreateBulk(bulk...).Save(ctx)
	require.NoError(t, err)

	// No enrichers registered: the pass still stamps LastEnrichedAt per artist.
	count, err := svc.EnrichArtists(ctx, u)
	require.NoError(t, err)
	assert.Equal(t, 100, count, "pass must be capped at the 100-artist batch limit")
}

// --- EnrichAlbums ---------------------------------------------------------

// TestEnrichAlbums_SelectionAndFieldMerge covers the album selection arms and
// enrichAlbum's merge semantics: first enricher in order wins contested
// fields, tags from all enrichers are merged and deduplicated, AI fields
// stamp LastAiEnrichedAt, and a failing enricher does not abort the album.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-013/020
func TestEnrichAlbums_SelectionAndFieldMerge(t *testing.T) {
	svc := newOrchestratorTestService(t)
	u := createTestUser(t, svc, "testuser")
	other := createTestUser(t, svc, "otheruser")
	ctx := context.Background()

	art, err := svc.client.Artist.Create().SetName("Album Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)
	otherArt, err := svc.client.Artist.Create().SetName("Other Artist").SetUser(other).Save(ctx)
	require.NoError(t, err)

	recent := time.Now().Add(-1 * time.Hour)

	_, err = svc.client.Album.Create().SetName("Fresh Album").SetUser(u).SetArtist(art).Save(ctx)
	require.NoError(t, err)
	_, err = svc.client.Album.Create().SetName("Fully Enriched Album").SetUser(u).SetArtist(art).
		SetLastEnrichedAt(recent).SetLastAiEnrichedAt(recent).SetLidarrID("7").Save(ctx)
	require.NoError(t, err)
	_, err = svc.client.Album.Create().SetName("Other User Album").SetUser(other).SetArtist(otherArt).Save(ctx)
	require.NoError(t, err)

	// Config order is musicbrainz -> spotify -> lastfm -> ... so register the
	// "first" data under musicbrainz, the "second" under spotify, and a
	// failing enricher under lastfm.
	first := &stubFullEnricher{
		typ: enrichers.TypeMusicBrainz,
		albumData: &enrichers.AlbumData{
			Year:  1970,
			Genre: "Rock",
			Tags:  []string{"classic", "guitar"},
		},
	}
	second := &stubFullEnricher{
		typ: enrichers.TypeSpotify,
		albumData: &enrichers.AlbumData{
			Genre:     "Pop", // contested: first enricher must win
			Label:     "Blue Note",
			Tags:      []string{"guitar", "jazz"}, // "guitar" deduped
			AISummary: "an album summary",
		},
	}
	failing := &stubFullEnricher{typ: enrichers.TypeLastFM, albumErr: fmt.Errorf("rate limited")}
	registerStub(t, svc, first)
	registerStub(t, svc, second)
	registerStub(t, svc, failing)

	count, err := svc.EnrichAlbums(ctx, u)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "only the fresh album qualifies")
	assert.Equal(t, []string{"Fresh Album"}, first.albumsSeen)
	assert.Equal(t, []string{"Fresh Album"}, second.albumsSeen)
	assert.Equal(t, []string{"Fresh Album"}, failing.albumsSeen,
		"failing enricher is still consulted; its error must not abort the album")

	got, err := svc.client.Album.Query().Where(albumByName("Fresh Album")).Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1970, got.Year)
	assert.Equal(t, "Rock", got.Genre, "first enricher in config order must win contested fields")
	assert.Equal(t, "Blue Note", got.Label, "uncontested fields from later enrichers must still apply")
	assert.ElementsMatch(t, []string{"classic", "guitar", "jazz"}, got.Tags, "tags must be merged and deduplicated")
	assert.Equal(t, "an album summary", got.AiSummary)
	assert.NotNil(t, got.LastAiEnrichedAt, "AI fields must stamp LastAiEnrichedAt")
	assert.False(t, got.LastEnrichedAt.IsZero())

	skipped, err := svc.client.Album.Query().Where(albumByName("Fully Enriched Album")).Only(ctx)
	require.NoError(t, err)
	assert.Empty(t, skipped.Genre, "fully enriched album must not be touched")

	assert.Equal(t, 1, syncEventCount(t, svc, syncevent.EventTypeAlbumEnriched))
}

// --- EnrichTracks -----------------------------------------------------------

// TestEnrichTracks_SelectionAndEnrichment covers the track selection arms
// (fresh, lidarr-status re-check, fully-enriched skip, other-user skip) and
// verifies enrichment fields plus the AI timestamp are persisted.
func TestEnrichTracks_SelectionAndEnrichment(t *testing.T) {
	svc := newOrchestratorTestService(t)
	u := createTestUser(t, svc, "testuser")
	other := createTestUser(t, svc, "otheruser")
	ctx := context.Background()

	art, err := svc.client.Artist.Create().SetName("Track Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)
	otherArt, err := svc.client.Artist.Create().SetName("Other Artist").SetUser(other).Save(ctx)
	require.NoError(t, err)

	recent := time.Now().Add(-1 * time.Hour)

	_, err = svc.client.Track.Create().SetName("Fresh Track").SetArtist(art).Save(ctx)
	require.NoError(t, err)
	// Fully stamped but Lidarr still reports "pending": must be re-selected.
	_, err = svc.client.Track.Create().SetName("Pending Track").SetArtist(art).
		SetLastEnrichedAt(recent).SetLastAiEnrichedAt(recent).SetLidarrID("9").SetLidarrStatus("pending").Save(ctx)
	require.NoError(t, err)
	// Fully stamped and imported in Lidarr: must be skipped.
	_, err = svc.client.Track.Create().SetName("Done Track").SetArtist(art).
		SetLastEnrichedAt(recent).SetLastAiEnrichedAt(recent).SetLidarrID("10").SetLidarrStatus("imported").Save(ctx)
	require.NoError(t, err)
	_, err = svc.client.Track.Create().SetName("Other User Track").SetArtist(otherArt).Save(ctx)
	require.NoError(t, err)

	stub := &stubFullEnricher{
		typ: enrichers.TypeSpotify,
		trackData: &enrichers.TrackData{
			SpotifyID:   "sp-123",
			ISRC:        "USRC17607839",
			DurationMs:  215000,
			TrackNumber: 3,
			Tags:        []string{"upbeat"},
			Genres:      []string{"pop", "Pop"}, // deduped case-insensitively
			AISummary:   "a track summary",
		},
	}
	failing := &stubFullEnricher{typ: enrichers.TypeLastFM, trackErr: fmt.Errorf("timeout")}
	registerStub(t, svc, stub)
	registerStub(t, svc, failing)

	count, err := svc.EnrichTracks(ctx, u)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
	assert.ElementsMatch(t, []string{"Fresh Track", "Pending Track"}, stub.tracksSeen,
		"selection must pick fresh and lidarr-pending tracks, and skip done/other-user tracks")

	got, err := svc.client.Track.Query().Where(trackByName("Fresh Track")).Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.SpotifyID)
	assert.Equal(t, "sp-123", *got.SpotifyID)
	require.NotNil(t, got.Isrc)
	assert.Equal(t, "USRC17607839", *got.Isrc)
	require.NotNil(t, got.DurationMs)
	assert.Equal(t, 215000, *got.DurationMs)
	require.NotNil(t, got.TrackNumber)
	assert.Equal(t, 3, *got.TrackNumber)
	assert.Equal(t, []string{"upbeat"}, got.Tags)
	assert.Equal(t, []string{"pop"}, got.Genres, "genres must be deduplicated case-insensitively")
	assert.Equal(t, "a track summary", got.AiSummary)
	assert.NotNil(t, got.LastAiEnrichedAt, "AI summary must stamp LastAiEnrichedAt (spotter-6p7)")
	assert.NotNil(t, got.LastEnrichedAt)

	assert.Equal(t, 2, syncEventCount(t, svc, syncevent.EventTypeTrackEnriched))
}

// --- BuildCatalog / processListenEntry / linkPlaylistTracks ------------------

// TestBuildCatalog_CreatesAndDeduplicatesEntities verifies get-or-create
// behavior across listens and playlist tracks: duplicates collapse into one
// catalog row, empty artists are skipped, album-less listens still create
// tracks, and playlist tracks are linked to their catalog entries.
func TestBuildCatalog_CreatesAndDeduplicatesEntities(t *testing.T) {
	svc := newOrchestratorTestService(t)
	u := createTestUser(t, svc, "testuser")
	ctx := context.Background()

	addListen := func(artist, album, track string) {
		_, err := svc.client.Listen.Create().
			SetUser(u).
			SetArtistName(artist).
			SetAlbumName(album).
			SetTrackName(track).
			SetSource("spotify").
			SetPlayedAt(time.Now()).
			Save(ctx)
		require.NoError(t, err)
	}

	addListen("Artist One", "Album One", "Track One")
	addListen("Artist One", "Album One", "Track One") // exact duplicate
	addListen("Artist One", "", "Track Two")          // no album
	addListen("", "Ghost Album", "Ghost Track")       // empty artist: skipped
	addListen("Artist Two", "Album Two", "Track Three")

	pl, err := svc.client.Playlist.Create().
		SetUser(u).
		SetRemoteID("pl-1").
		SetName("My Playlist").
		SetSource("spotify").
		Save(ctx)
	require.NoError(t, err)

	// Duplicate of a listen entry plus a playlist-only track.
	_, err = svc.client.PlaylistTrack.Create().SetPosition(0).
		SetPlaylist(pl).SetArtistName("Artist One").SetAlbumName("Album One").SetTrackName("Track One").Save(ctx)
	require.NoError(t, err)
	_, err = svc.client.PlaylistTrack.Create().SetPosition(1).
		SetPlaylist(pl).SetArtistName("Artist Three").SetAlbumName("Album Three").SetTrackName("Track Four").Save(ctx)
	require.NoError(t, err)

	require.NoError(t, svc.BuildCatalog(ctx, u))

	artistCount, err := svc.client.Artist.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, artistCount, "Artist One, Artist Two, Artist Three")

	albumCount, err := svc.client.Album.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, albumCount, "Album One, Album Two, Album Three; Ghost Album skipped")

	trackCount, err := svc.client.Track.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 4, trackCount, "Tracks One-Four; Ghost Track skipped")

	// Both playlist tracks must be linked to catalog entries.
	linked, err := svc.client.PlaylistTrack.Query().Where(playlisttrack.HasTrack()).Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, linked, "all playlist tracks must be linked to catalog tracks")

	assert.Equal(t, 1, syncEventCount(t, svc, syncevent.EventTypeCatalogBuilt))

	// A second run must be idempotent: nothing new is created.
	require.NoError(t, svc.BuildCatalog(ctx, u))
	artistCount2, err := svc.client.Artist.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, artistCount2)
	trackCount2, err := svc.client.Track.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 4, trackCount2)
}

// TestLinkPlaylistTracks_LinksOnlyMatchingAndSkipsLinked verifies that only
// playlist tracks with a matching (artist, track) catalog entry get linked,
// that artist/album edges are propagated, and that already-linked tracks are
// not reprocessed.
func TestLinkPlaylistTracks_LinksOnlyMatchingAndSkipsLinked(t *testing.T) {
	svc := newOrchestratorTestService(t)
	u := createTestUser(t, svc, "testuser")
	ctx := context.Background()

	art, err := svc.client.Artist.Create().SetName("Known Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)
	alb, err := svc.client.Album.Create().SetName("Known Album").SetUser(u).SetArtist(art).Save(ctx)
	require.NoError(t, err)
	_, err = svc.client.Track.Create().SetName("Known Track").SetArtist(art).SetAlbum(alb).Save(ctx)
	require.NoError(t, err)

	pl, err := svc.client.Playlist.Create().
		SetUser(u).SetRemoteID("pl-1").SetName("Playlist").SetSource("spotify").Save(ctx)
	require.NoError(t, err)

	matched, err := svc.client.PlaylistTrack.Create().SetPosition(0).
		SetPlaylist(pl).SetArtistName("Known Artist").SetAlbumName("Known Album").SetTrackName("Known Track").Save(ctx)
	require.NoError(t, err)
	unmatched, err := svc.client.PlaylistTrack.Create().SetPosition(1).
		SetPlaylist(pl).SetArtistName("Unknown Artist").SetTrackName("Unknown Track").Save(ctx)
	require.NoError(t, err)

	linked, err := svc.linkPlaylistTracks(ctx, u)
	require.NoError(t, err)
	assert.Equal(t, 1, linked)

	gotTrack, err := matched.QueryTrack().Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Known Track", gotTrack.Name)
	gotArtist, err := matched.QueryArtist().Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Known Artist", gotArtist.Name)
	gotAlbum, err := matched.QueryAlbum().Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Known Album", gotAlbum.Name)

	hasTrack, err := unmatched.QueryTrack().Exist(ctx)
	require.NoError(t, err)
	assert.False(t, hasTrack, "playlist track without a catalog match must stay unlinked")

	// Second run: the already-linked track is excluded by the query.
	linked, err = svc.linkPlaylistTracks(ctx, u)
	require.NoError(t, err)
	assert.Equal(t, 0, linked, "already-linked playlist tracks must not be re-linked")
}

// TestEnrichNewListens covers the metadata-disabled short-circuit, the
// creates-catalog-entries path, and the nothing-new-added path.
func TestEnrichNewListens(t *testing.T) {
	svc := newOrchestratorTestService(t)
	u := createTestUser(t, svc, "testuser")
	ctx := context.Background()

	// Disabled: nothing is created.
	svc.config.Metadata.Enabled = false
	svc.EnrichNewListens(ctx, u, "New Artist", "New Album", "New Track")
	n, err := svc.client.Artist.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "disabled metadata must not create catalog entries")

	// Enabled: creates artist, album, track.
	svc.config.Metadata.Enabled = true
	svc.EnrichNewListens(ctx, u, "New Artist", "New Album", "New Track")

	n, err = svc.client.Artist.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	n, err = svc.client.Album.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	n, err = svc.client.Track.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// Repeat: nothing new is added (dedup path).
	svc.EnrichNewListens(ctx, u, "New Artist", "New Album", "New Track")
	n, err = svc.client.Track.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "repeated listen must not create duplicates")
}

// TestProcessListenEntry_EmptyArtistIsNoop verifies the guard for listens with
// no artist name.
func TestProcessListenEntry_EmptyArtistIsNoop(t *testing.T) {
	svc := newOrchestratorTestService(t)
	u := createTestUser(t, svc, "testuser")

	added, err := svc.processListenEntry(context.Background(), u, "", "Album", "Track")
	require.NoError(t, err)
	assert.Nil(t, added)
}

// --- SyncAll ------------------------------------------------------------------

// TestSyncAll_DisabledIsNoop verifies the enabled-flag short circuit.
func TestSyncAll_DisabledIsNoop(t *testing.T) {
	svc := newOrchestratorTestService(t)
	svc.config.Metadata.Enabled = false
	u := createTestUser(t, svc, "testuser")

	require.NoError(t, svc.SyncAll(context.Background(), u))
	assert.Equal(t, 0, syncEventCount(t, svc, syncevent.EventTypeMetadataStarted),
		"disabled metadata sync must not log a start event")
}

// TestSyncAll_UserRefreshFailure verifies that a user that cannot be reloaded
// fails the sync with a metadata_failed-style error return.
func TestSyncAll_UserRefreshFailure(t *testing.T) {
	svc := newOrchestratorTestService(t)

	ghost := &ent.User{ID: 999999}
	err := svc.SyncAll(context.Background(), ghost)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to refresh user")
}

// TestSyncAll_FullPassWithStubEnricher runs the whole SyncAll shell against
// stub enrichers: catalog is built from listens, listens are matched, and all
// three enrichment passes plus the (empty) image download step run.
func TestSyncAll_FullPassWithStubEnricher(t *testing.T) {
	svc := newOrchestratorTestService(t)
	svc.config.Metadata.Images.Download = true
	svc.config.Metadata.Images.Directory = t.TempDir()
	u := createTestUser(t, svc, "testuser")
	ctx := context.Background()

	_, err := svc.client.Listen.Create().
		SetUser(u).
		SetArtistName("Sync Artist").
		SetAlbumName("Sync Album").
		SetTrackName("Sync Track").
		SetSource("navidrome").
		SetPlayedAt(time.Now()).
		Save(ctx)
	require.NoError(t, err)

	stub := &stubFullEnricher{
		typ:        enrichers.TypeMusicBrainz,
		artistData: &enrichers.ArtistData{Bio: "synced bio"},
		albumData:  &enrichers.AlbumData{Year: 1999},
		trackData:  &enrichers.TrackData{DurationMs: 1000},
	}
	registerStub(t, svc, stub)

	require.NoError(t, svc.SyncAll(ctx, u))

	// Catalog built from the listen.
	art, err := svc.client.Artist.Query().Where(artistByName("Sync Artist")).Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, "synced bio", art.Bio)

	alb, err := svc.client.Album.Query().Where(albumByName("Sync Album")).Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1999, alb.Year)

	trk, err := svc.client.Track.Query().Where(trackByName("Sync Track")).Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, trk.DurationMs)
	assert.Equal(t, 1000, *trk.DurationMs)

	// The listen got matched to the new catalog entities.
	listens, err := svc.client.Listen.Query().WithTrack().WithArtist().WithAlbum().All(ctx)
	require.NoError(t, err)
	require.Len(t, listens, 1)
	assert.NotNil(t, listens[0].Edges.Artist, "listen must be matched to its artist")
	assert.NotNil(t, listens[0].Edges.Album, "listen must be matched to its album")
	assert.NotNil(t, listens[0].Edges.Track, "listen must be matched to its track")

	// Lifecycle events logged.
	assert.Equal(t, 1, syncEventCount(t, svc, syncevent.EventTypeMetadataStarted))
	assert.Equal(t, 1, syncEventCount(t, svc, syncevent.EventTypeCatalogBuilt))
	assert.Equal(t, 1, syncEventCount(t, svc, syncevent.EventTypeMetadataCompleted))
	assert.Equal(t, 0, syncEventCount(t, svc, syncevent.EventTypeMetadataFailed))
}
