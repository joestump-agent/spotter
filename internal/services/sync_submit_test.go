// Governing: SPEC music-provider-integration REQ "ListenBrainz Listen Submission" (REQ-PROV-054)
// Tests for the syncer's listen-submission phase: opt-in gating, provenance
// filtering (ListenBrainz-origin listens are never echoed back), submitted-flag
// idempotence across sync rounds, batching at the API limit, and tolerance of
// submission failures.
package services_test

import (
	"context"
	"errors"
	"fmt"
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
// providers.ListenSubmitter, recording every batch it receives. submitFn, when
// set, decides per call whether the batch is accepted (nil) or rejected
// (non-nil error); accepted batches are recorded.
type mockListenSubmitter struct {
	providerType providers.Type
	batches      [][]providers.Track
	err          error
	submitFn     func(listens []providers.Track) error
	calls        int
}

func (m *mockListenSubmitter) Type() providers.Type {
	return m.providerType
}

func (m *mockListenSubmitter) SubmitListens(_ context.Context, listens []providers.Track) error {
	m.calls++
	if m.submitFn != nil {
		if err := m.submitFn(listens); err != nil {
			return err
		}
	} else if m.err != nil {
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

// Governing: SPEC music-provider-integration REQ-PROV-054 (provenance: listens
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

// Governing: SPEC music-provider-integration REQ-PROV-054 (idempotence: the
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

// Governing: SPEC music-provider-integration REQ-PROV-054 (batching at the API
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

// Governing: SPEC music-provider-integration REQ-PROV-054 (failure tolerance:
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

// Regression: PR #55 adversarial review MAJOR 1 — a "poison" listen that
// ListenBrainz permanently rejects (400 on the whole batch) used to wedge
// submission forever: the chunk error stamped nothing, so the oldest-first
// NULL-flag query re-selected the SAME chunk on every sync, sending one doomed
// request per run. The syncer must fall back to per-listen submission on a
// non-429 4xx, submit every valid listen, stamp the rejected row as processed
// (the flag means "processed", not "accepted"), and leave nothing behind for
// the next round.
func TestSyncer_SubmitListens_Regression_PoisonListenDoesNotWedgeSubmission(t *testing.T) {
	client, syncer, _ := setupTestSyncer(t)
	ctx := context.Background()
	u := createListenBrainzUser(t, client, true)

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	seedListen(t, client, u, "navidrome", "Valid Before", base)
	poison := seedListen(t, client, u, "spotify", "Poison Track", base.Add(time.Hour))
	seedListen(t, client, u, "lastfm", "Valid After", base.Add(2*time.Hour))

	// Rejects any batch containing the poison listen with the non-retryable
	// 4xx that POST /1/submit-listens returns for invalid listens.
	rejection := fmt.Errorf("failed to submit listens to listenbrainz: listenbrainz api returned %w",
		&providers.StatusError{StatusCode: 400, Body: `{"code":400,"error":"invalid listen"}`})
	submitter := &mockListenSubmitter{
		providerType: providers.TypeListenBrainz,
		submitFn: func(listens []providers.Track) error {
			for _, l := range listens {
				if l.Name == "Poison Track" {
					return rejection
				}
			}
			return nil
		},
	}
	syncer.Register(mockFactory(submitter))

	// Round 1: the sync itself succeeds and every valid listen is submitted
	// despite the poison listen sitting mid-chunk.
	require.NoError(t, syncer.Sync(ctx, u), "a rejected listen must not fail the sync")
	got := submitter.received()
	require.Len(t, got, 2, "all valid listens in the chunk must be submitted")
	names := map[string]bool{}
	for _, tr := range got {
		names[tr.Name] = true
	}
	assert.True(t, names["Valid Before"])
	assert.True(t, names["Valid After"])
	assert.False(t, names["Poison Track"], "the rejected listen is never recorded as accepted")

	// The poison row is stamped as processed so it can never wedge the queue.
	refreshed, err := client.Listen.Get(ctx, poison.ID)
	require.NoError(t, err)
	assert.NotNil(t, refreshed.SubmittedToListenbrainzAt,
		"a permanently rejected listen must be stamped as processed")

	unflagged, err := client.Listen.Query().
		Where(listen.HasUserWith(user.ID(u.ID)), listen.SubmittedToListenbrainzAtIsNil()).
		Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, unflagged, "every listen in the chunk must be processed")

	// Round 2: nothing is selected — the poison chunk is not retried.
	submitter.batches = nil
	submitter.calls = 0
	require.NoError(t, syncer.Sync(ctx, u))
	assert.Zero(t, submitter.calls, "next sync must not re-select the poisoned chunk")
}

// Regression: PR #55 adversarial review MAJOR 2 — cross-provider dedup defeats
// provenance. When the user's player scrobbles to ListenBrainz natively, the
// play exists locally twice conceptually but only the spotify/navidrome row
// survives persistence dedup, or both rows coexist; either way the non-LB row
// (NULL flag) used to be submitted BACK to ListenBrainz, duplicating every
// play in the user's LB history. A listen with a listenbrainz-source sibling
// (same track identity within the ±2min dedup window) must be skipped and
// stamped as processed instead of submitted.
func TestSyncer_SubmitListens_Regression_ListenBrainzSiblingNotEchoedBack(t *testing.T) {
	client, syncer, _ := setupTestSyncer(t)
	ctx := context.Background()
	u := createListenBrainzUser(t, client, true)

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// A spotify play whose LB-source sibling (same track identity, 30s apart)
	// proves ListenBrainz already has this listen natively.
	shared, err := client.Listen.Create().
		SetUser(u).
		SetTrackName("Shared Song").
		SetArtistName("Shared Artist").
		SetAlbumName("Shared Album").
		SetSource("spotify").
		SetPlayedAt(base).
		Save(ctx)
	require.NoError(t, err)
	sibling, err := client.Listen.Create().
		SetUser(u).
		SetTrackName("Shared Song").
		SetArtistName("Shared Artist").
		SetAlbumName("Shared Album").
		SetSource("listenbrainz").
		SetPlayedAt(base.Add(30 * time.Second)).
		Save(ctx)
	require.NoError(t, err)

	// A spotify play with no LB sibling: must be submitted as usual.
	solo, err := client.Listen.Create().
		SetUser(u).
		SetTrackName("Solo Song").
		SetArtistName("Solo Artist").
		SetAlbumName("Solo Album").
		SetSource("spotify").
		SetPlayedAt(base.Add(time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	submitter := &mockListenSubmitter{providerType: providers.TypeListenBrainz}
	syncer.Register(mockFactory(submitter))

	require.NoError(t, syncer.Sync(ctx, u))

	got := submitter.received()
	require.Len(t, got, 1, "only the listen WITHOUT a ListenBrainz sibling is submitted")
	assert.Equal(t, "Solo Song", got[0].Name)

	// The sibling-suppressed row is stamped as processed (never selected again).
	refreshedShared, err := client.Listen.Get(ctx, shared.ID)
	require.NoError(t, err)
	assert.NotNil(t, refreshedShared.SubmittedToListenbrainzAt,
		"a listen already present in LB via its sibling must be stamped as processed")

	refreshedSolo, err := client.Listen.Get(ctx, solo.ID)
	require.NoError(t, err)
	assert.NotNil(t, refreshedSolo.SubmittedToListenbrainzAt)

	// The listenbrainz-origin sibling itself stays out of the pipeline
	// entirely (provenance filter) and is never stamped.
	refreshedSibling, err := client.Listen.Get(ctx, sibling.ID)
	require.NoError(t, err)
	assert.Nil(t, refreshedSibling.SubmittedToListenbrainzAt)

	// Round 2: nothing left to submit.
	submitter.batches = nil
	submitter.calls = 0
	require.NoError(t, syncer.Sync(ctx, u))
	assert.Zero(t, submitter.calls)
}

// Governing: SPEC music-provider-integration REQ-PROV-054 (submission is
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
