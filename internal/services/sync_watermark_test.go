// Governing: issue #50 (history sync: mid-pagination failure must not leave a
// permanent gap), SPEC graceful-shutdown REQ-REC-001, REQ-REC-002 (idempotent
// listen sync via timestamp watermark), SPEC listen-playlist-sync REQ-SYNC-020
// (since timestamp from last listen), REQ-SYNC-021 (idempotent dedup).
package services_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"spotter/ent"
	"spotter/ent/listen"
	"spotter/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// paginatingHistoryProvider simulates a provider that delivers its history in
// NEWEST-FIRST pages via the batched callback contract, like ListenBrainz and
// Last.fm. page1 (the newest listens) is always delivered; page2 (older
// listens) is delivered only when failPage2 is false, otherwise the walk fails
// AFTER page1 was handed to the callback — the exact mid-pagination failure
// mode from issue #50. Every `since` value the syncer passes in is recorded so
// tests can assert whether the watermark advanced.
type paginatingHistoryProvider struct {
	providerType  providers.Type
	page1         []providers.Track // newest page, always delivered
	page2         []providers.Track // older page, delivered only when !failPage2
	failPage2     bool
	receivedSince []time.Time
}

func (m *paginatingHistoryProvider) Type() providers.Type {
	return m.providerType
}

func (m *paginatingHistoryProvider) GetRecentListens(ctx context.Context, since time.Time, callback func([]providers.Track) error) error {
	m.receivedSince = append(m.receivedSince, since)

	// Page 1 (newest) is delivered first, mirroring newest-first pagination.
	if len(m.page1) > 0 {
		if err := callback(m.page1); err != nil {
			return err
		}
	}

	// The older page fails mid-walk: page 1 has already been handed to the
	// callback, so a naive per-batch persist would have committed the newest
	// listens and advanced the watermark past the range page 2 would cover.
	if m.failPage2 {
		return fmt.Errorf("page 2 fetch failed: connection reset")
	}

	if len(m.page2) > 0 {
		if err := callback(m.page2); err != nil {
			return err
		}
	}
	return nil
}

// TestSyncHistory_MidWalkFailure_DoesNotAdvanceWatermark is the issue #50
// regression test: when a newest-first history walk delivers page 1 and then
// fails on page 2, the watermark (max stored PlayedAt) must NOT advance past
// the unfetched older range. Concretely, page 1's newest listens must not be
// persisted on the failed sync, and the next sync must re-fetch from the same
// `since` (below page 1's timestamps) so the previously unfetched range is
// picked up. Without the buffer-then-persist-oldest-first fix, page 1 is
// committed immediately, the watermark jumps to page 1's newest PlayedAt, and
// page 2's range is lost forever.
func TestSyncHistory_MidWalkFailure_DoesNotAdvanceWatermark(t *testing.T) {
	client, syncer, _ := setupTestSyncer(t)
	ctx := context.Background()
	user := createTestUser(t, client)

	// page1 holds the NEWEST listens; page2 the older ones. Both are well
	// inside the default 720h lookback so the initial `since` is far below
	// either page's timestamps.
	page1Time := time.Now().Add(-1 * time.Minute).UTC().Truncate(time.Second)
	page2Time := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Second)

	prov := &paginatingHistoryProvider{
		providerType: providers.TypeListenBrainz,
		page1: []providers.Track{
			{ID: "new-1", Name: "Newest Song", Artist: "Artist A", Album: "Album A", PlayedAt: page1Time},
		},
		page2: []providers.Track{
			{ID: "old-1", Name: "Older Song", Artist: "Artist B", Album: "Album B", PlayedAt: page2Time},
		},
		failPage2: true,
	}
	syncer.Register(mockFactory(prov))

	// First sync: page 1 is delivered, then page 2 fails mid-walk.
	err := syncer.SyncRecentListens(ctx, user)
	require.Error(t, err, "a mid-walk pagination failure must surface as an error")

	// The watermark must not have advanced: nothing was persisted, because the
	// walk did not complete. Persisting page 1 here is exactly the bug — it
	// would leave a permanent gap below its high watermark.
	listens, err := client.Listen.Query().All(ctx)
	require.NoError(t, err)
	require.Empty(t, listens, "a failed history walk must persist nothing, so the watermark cannot advance past the unfetched range")

	require.Len(t, prov.receivedSince, 1)
	firstSince := prov.receivedSince[0]
	assert.True(t, firstSince.Before(page1Time), "sanity: the initial lookback watermark is below page 1")

	// The retriable failure tripped a backoff window. Simulate the user's next
	// sync tick reaching the provider again (the watermark logic under test is
	// independent of backoff timing).
	syncer.ClearProviderBackoff(user.ID, providers.TypeListenBrainz)

	// Second sync: the provider recovers and the full walk (page 1 + page 2)
	// completes.
	prov.failPage2 = false
	err = syncer.SyncRecentListens(ctx, user)
	require.NoError(t, err, "the recovered provider must sync cleanly")

	// The watermark did NOT advance past the unfetched range: the second sync
	// re-fetched from a `since` still below page 1's timestamp, so the older
	// page 2 range is re-covered rather than skipped.
	require.Len(t, prov.receivedSince, 2)
	secondSince := prov.receivedSince[1]
	assert.True(t, secondSince.Before(page1Time),
		"next sync must re-fetch from a watermark below page 1 (the gap must not have been skipped)")
	assert.True(t, secondSince.Before(page2Time),
		"next sync must re-fetch the previously unfetched older range")

	// Both pages are now persisted — no permanent gap, and dedup kept page 1
	// from being duplicated by its re-delivery.
	listens, err = client.Listen.Query().Order(ent.Asc(listen.FieldPlayedAt)).All(ctx)
	require.NoError(t, err)
	require.Len(t, listens, 2, "after recovery both the newest and the previously unfetched older listens must be present exactly once")
	assert.Equal(t, "Older Song", listens[0].TrackName, "oldest-first persistence order")
	assert.Equal(t, "Newest Song", listens[1].TrackName)
}

// TestSyncHistory_MidWalkFailure_Idempotent_NoDuplicateOnReFetch guards the
// dedup half of the fix: re-fetching after a mid-walk failure re-delivers the
// newest page, and the idempotent persist layer must not create duplicates.
// Governing: SPEC listen-playlist-sync REQ-SYNC-021 (dedup by
// provider+provider_track_id+played_at).
func TestSyncHistory_MidWalkFailure_Idempotent_NoDuplicateOnReFetch(t *testing.T) {
	client, syncer, _ := setupTestSyncer(t)
	ctx := context.Background()
	user := createTestUser(t, client)

	page1Time := time.Now().Add(-2 * time.Minute).UTC().Truncate(time.Second)
	page2Time := time.Now().Add(-20 * time.Minute).UTC().Truncate(time.Second)

	prov := &paginatingHistoryProvider{
		providerType: providers.TypeLastFM,
		page1: []providers.Track{
			{ID: "lfm-new", Name: "Recent", Artist: "A", PlayedAt: page1Time},
		},
		page2: []providers.Track{
			{ID: "lfm-old", Name: "Ancient", Artist: "B", PlayedAt: page2Time},
		},
		failPage2: true,
	}
	syncer.Register(mockFactory(prov))

	require.Error(t, syncer.SyncRecentListens(ctx, user))
	syncer.ClearProviderBackoff(user.ID, providers.TypeLastFM)

	prov.failPage2 = false
	require.NoError(t, syncer.SyncRecentListens(ctx, user))

	// A third, fully successful sync re-delivers both pages again; dedup must
	// keep the row count stable.
	syncer.ClearProviderBackoff(user.ID, providers.TypeLastFM)
	require.NoError(t, syncer.SyncRecentListens(ctx, user))

	count, err := client.Listen.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, count, "repeated re-delivery of the same listens must not create duplicates")
}
