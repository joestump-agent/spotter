// Governing: SPEC music-provider-integration REQ "ListenBrainz Listen Submission" (REQ-PROV-049)
// Tests for the syncer's listen-submission phase: opt-in gating, provenance
// filtering (ListenBrainz-origin listens are never echoed back), submitted-flag
// idempotence across sync rounds, batching at the API limit, and tolerance of
// submission failures.
package services_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"spotter/ent"
	"spotter/ent/listen"
	"spotter/ent/user"
	"spotter/internal/providers"
	"spotter/internal/providers/listenbrainz"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockListenSubmitter implements providers.Provider and
// providers.ListenSubmitter, recording every batch it receives.
type mockListenSubmitter struct {
	providerType providers.Type
	batches      [][]providers.Track
	err          error
}

func (m *mockListenSubmitter) Type() providers.Type {
	return m.providerType
}

func (m *mockListenSubmitter) SubmitListens(_ context.Context, listens []providers.Track) error {
	if m.err != nil {
		return m.err
	}
	batch := make([]providers.Track, len(listens))
	copy(batch, listens)
	m.batches = append(m.batches, batch)
	return nil
}

func (m *mockListenSubmitter) received() []providers.Track {
	var all []providers.Track
	for _, b := range m.batches {
		all = append(all, b...)
	}
	return all
}

// interface satisfaction for the mock itself
var _ providers.ListenSubmitter = (*mockListenSubmitter)(nil)

// createListenBrainzUser creates a user with a ListenBrainzAuth edge with the
// given submit_listens opt-in state.
func createListenBrainzUser(t *testing.T, client *ent.Client, submitListens bool) *ent.User {
	t.Helper()
	ctx := context.Background()
	u := createTestUser(t, client)
	_, err := client.ListenBrainzAuth.Create().
		SetUser(u).
		SetToken("lb-token").
		SetUsername("lb-user").
		SetSubmitListens(submitListens).
		Save(ctx)
	require.NoError(t, err)
	return u
}

// seedListen persists one listen row for the user with the given source.
func seedListen(t *testing.T, client *ent.Client, u *ent.User, source, track string, playedAt time.Time) *ent.Listen {
	t.Helper()
	l, err := client.Listen.Create().
		SetUser(u).
		SetTrackName(track).
		SetArtistName("Artist " + track).
		SetAlbumName("Album " + track).
		SetSource(source).
		SetPlayedAt(playedAt).
		Save(context.Background())
	require.NoError(t, err)
	return l
}

// Governing: SPEC music-provider-integration REQ-PROV-049 (provenance: listens
// that came FROM ListenBrainz are never submitted back to ListenBrainz)
func TestSyncer_SubmitListens_ProvenanceFilter(t *testing.T) {
	client, syncer, _ := setupTestSyncer(t)
	ctx := context.Background()
	u := createListenBrainzUser(t, client, true)

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	seedListen(t, client, u, "navidrome", "From Navidrome", base)
	seedListen(t, client, u, "spotify", "From Spotify", base.Add(time.Hour))
	seedListen(t, client, u, "lastfm", "From LastFM", base.Add(2*time.Hour))
	lbListen := seedListen(t, client, u, "listenbrainz", "From ListenBrainz", base.Add(3*time.Hour))

	submitter := &mockListenSubmitter{providerType: providers.TypeListenBrainz}
	syncer.Register(mockFactory(submitter))

	require.NoError(t, syncer.Sync(ctx, u))

	got := submitter.received()
	require.Len(t, got, 3, "only listens from OTHER sources are submitted")
	names := map[string]bool{}
	for _, tr := range got {
		names[tr.Name] = true
	}
	assert.True(t, names["From Navidrome"])
	assert.True(t, names["From Spotify"])
	assert.True(t, names["From LastFM"])
	assert.False(t, names["From ListenBrainz"], "a ListenBrainz-origin listen must NEVER be echoed back")

	// The ListenBrainz-origin row must remain unflagged (it was never submitted).
	refreshed, err := client.Listen.Get(ctx, lbListen.ID)
	require.NoError(t, err)
	assert.Nil(t, refreshed.SubmittedToListenbrainzAt)
}

// Governing: SPEC music-provider-integration REQ-PROV-049 (idempotence: the
// submitted flag persists, so a second sync round submits nothing new)
func TestSyncer_SubmitListens_FlagPersistsAcrossRounds(t *testing.T) {
	client, syncer, _ := setupTestSyncer(t)
	ctx := context.Background()
	u := createListenBrainzUser(t, client, true)

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	seedListen(t, client, u, "navidrome", "Track A", base)
	seedListen(t, client, u, "spotify", "Track B", base.Add(time.Hour))

	submitter := &mockListenSubmitter{providerType: providers.TypeListenBrainz}
	syncer.Register(mockFactory(submitter))

	// Round 1: both listens are submitted and flagged.
	require.NoError(t, syncer.Sync(ctx, u))
	require.Len(t, submitter.received(), 2)

	flagged, err := client.Listen.Query().
		Where(
			listen.HasUserWith(user.ID(u.ID)),
			listen.SubmittedToListenbrainzAtNotNil(),
		).
		Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, flagged, "submitted listens must be flagged")

	// Round 2: nothing new to submit — no additional batches.
	submitter.batches = nil
	require.NoError(t, syncer.Sync(ctx, u))
	assert.Empty(t, submitter.batches, "second round must not resubmit already-submitted listens")

	// A NEW listen appearing later is submitted on the next round.
	seedListen(t, client, u, "navidrome", "Track C", base.Add(2*time.Hour))
	require.NoError(t, syncer.Sync(ctx, u))
	got := submitter.received()
	require.Len(t, got, 1)
	assert.Equal(t, "Track C", got[0].Name)
}

// Governing: SPEC music-provider-integration REQ-PROV-049 (batching at the API
// limit: chunks of at most listenbrainz.MaxListensPerRequest per call)
func TestSyncer_SubmitListens_BatchesAtAPILimit(t *testing.T) {
	client, syncer, _ := setupTestSyncer(t)
	ctx := context.Background()
	u := createListenBrainzUser(t, client, true)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	n := listenbrainz.MaxListensPerRequest + 3
	builders := make([]*ent.ListenCreate, 0, n)
	for i := 0; i < n; i++ {
		builders = append(builders, client.Listen.Create().
			SetUser(u).
			SetTrackName("Bulk Track").
			SetArtistName("Bulk Artist").
			SetAlbumName("Bulk Album").
			SetSource("navidrome").
			SetPlayedAt(base.Add(time.Duration(i)*time.Second)))
	}
	_, err := client.Listen.CreateBulk(builders...).Save(ctx)
	require.NoError(t, err)

	submitter := &mockListenSubmitter{providerType: providers.TypeListenBrainz}
	syncer.Register(mockFactory(submitter))

	require.NoError(t, syncer.Sync(ctx, u))

	require.Len(t, submitter.batches, 2, "listens must be pushed in API-limit chunks")
	assert.Len(t, submitter.batches[0], listenbrainz.MaxListensPerRequest)
	assert.Len(t, submitter.batches[1], 3)

	// Oldest-first ordering within the run.
	first := submitter.batches[0][0]
	assert.Equal(t, base, first.PlayedAt.UTC())

	unflagged, err := client.Listen.Query().
		Where(listen.HasUserWith(user.ID(u.ID)), listen.SubmittedToListenbrainzAtIsNil()).
		Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, unflagged, "every submitted listen must be flagged")
}

// Governing: SPEC music-provider-integration REQ-PROV-049 (failure tolerance:
// a submission failure never fails the sync, flags stay unset, and the next
// round retries the unsubmitted listens)
func TestSyncer_SubmitListens_FailureToleratedAndRetriedNextRound(t *testing.T) {
	client, syncer, _ := setupTestSyncer(t)
	ctx := context.Background()
	u := createListenBrainzUser(t, client, true)

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	seedListen(t, client, u, "navidrome", "Track A", base)

	submitter := &mockListenSubmitter{
		providerType: providers.TypeListenBrainz,
		err:          errors.New("listenbrainz api returned status 503"),
	}
	syncer.Register(mockFactory(submitter))

	// Round 1: submission fails — the sync itself still succeeds.
	require.NoError(t, syncer.Sync(ctx, u), "a submission failure must not fail the whole sync")

	flagged, err := client.Listen.Query().
		Where(listen.HasUserWith(user.ID(u.ID)), listen.SubmittedToListenbrainzAtNotNil()).
		Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, flagged, "failed submissions must NOT be flagged as submitted")

	// Round 2: the API recovers and the same listen is retried and flagged.
	submitter.err = nil
	require.NoError(t, syncer.Sync(ctx, u))
	got := submitter.received()
	require.Len(t, got, 1)
	assert.Equal(t, "Track A", got[0].Name)

	flagged, err = client.Listen.Query().
		Where(listen.HasUserWith(user.ID(u.ID)), listen.SubmittedToListenbrainzAtNotNil()).
		Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, flagged)
}

// Governing: SPEC music-provider-integration REQ-PROV-049 (submission is
// opt-in and defaults OFF)
func TestSyncer_SubmitListens_OptInGate(t *testing.T) {
	client, syncer, _ := setupTestSyncer(t)
	ctx := context.Background()

	// submit_listens=false (the schema default): nothing is submitted even
	// though the user has unsubmitted listens from other sources.
	u := createListenBrainzUser(t, client, false)
	seedListen(t, client, u, "navidrome", "Track A", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	submitter := &mockListenSubmitter{providerType: providers.TypeListenBrainz}
	syncer.Register(mockFactory(submitter))

	require.NoError(t, syncer.Sync(ctx, u))
	assert.Empty(t, submitter.batches, "submission is opt-in and must default OFF")
}

// A user without any ListenBrainz connection is never submitted for, even if
// a ListenSubmitter provider is registered.
func TestSyncer_SubmitListens_NoAuthNoSubmission(t *testing.T) {
	client, syncer, _ := setupTestSyncer(t)
	ctx := context.Background()

	u := createTestUser(t, client)
	seedListen(t, client, u, "navidrome", "Track A", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	submitter := &mockListenSubmitter{providerType: providers.TypeListenBrainz}
	syncer.Register(mockFactory(submitter))

	require.NoError(t, syncer.Sync(ctx, u))
	assert.Empty(t, submitter.batches)
}

// SyncProvider targeting ListenBrainz also runs the submission phase.
func TestSyncer_SubmitListens_SyncProvider(t *testing.T) {
	client, syncer, _ := setupTestSyncer(t)
	ctx := context.Background()
	u := createListenBrainzUser(t, client, true)
	seedListen(t, client, u, "spotify", "Track A", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))

	submitter := &mockListenSubmitter{providerType: providers.TypeListenBrainz}
	syncer.Register(mockFactory(submitter))

	require.NoError(t, syncer.SyncProvider(ctx, u, providers.TypeListenBrainz))
	got := submitter.received()
	require.Len(t, got, 1)
	assert.Equal(t, "Track A", got[0].Name)
}
