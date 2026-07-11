package handlers

import (
	"errors"
	"strings"
	"testing"

	"spotter/internal/providers"
	"spotter/internal/services"

	"github.com/stretchr/testify/assert"
)

// Governing: ADR-0020; issue #36 (sync UX) — the reset flow's toast selection is
// pure so partial/total/complete branching is unit-testable without the handler.

func failedResult(types ...providers.Type) *services.SyncResult {
	res := &services.SyncResult{}
	for _, t := range types {
		res.Providers = append(res.Providers, services.ProviderResult{Provider: t, Outcome: services.ProviderSyncFailed})
	}
	return res
}

func TestResetSyncShouldEnrich(t *testing.T) {
	syncErr := errors.New("boom")

	tests := []struct {
		name    string
		res     *services.SyncResult
		syncErr error
		want    bool
	}{
		{"full success enriches", failedResult(), nil, true},
		{
			name: "partial success enriches",
			res: &services.SyncResult{Providers: []services.ProviderResult{
				{Provider: providers.TypeSpotify, Outcome: services.ProviderSyncFailed},
				{Provider: providers.TypeNavidrome, Outcome: services.ProviderSynced},
			}},
			syncErr: syncErr,
			want:    true,
		},
		{"total failure skips enrichment", failedResult(providers.TypeSpotify), syncErr, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, resetSyncShouldEnrich(tc.res, tc.syncErr))
		})
	}
}

func TestResetSyncNotification(t *testing.T) {
	syncErr := errors.New("provider failed")
	metaErr := errors.New("enrichment failed")

	partial := &services.SyncResult{Providers: []services.ProviderResult{
		{Provider: providers.TypeSpotify, Outcome: services.ProviderSyncFailed},
		{Provider: providers.TypeNavidrome, Outcome: services.ProviderSynced},
	}}
	allSucceeded := &services.SyncResult{Providers: []services.ProviderResult{
		{Provider: providers.TypeNavidrome, Outcome: services.ProviderSynced},
	}}
	backingOff := &services.SyncResult{Providers: []services.ProviderResult{
		{Provider: providers.TypeNavidrome, Outcome: services.ProviderSynced},
		{Provider: providers.TypeSpotify, Outcome: services.ProviderBackingOff},
	}}

	t.Run("total failure names failing providers", func(t *testing.T) {
		got := resetSyncNotification(failedResult(providers.TypeSpotify, providers.TypeLastFM), syncErr, nil)
		assert.Equal(t, "Reset Failed", got.Title)
		assert.Equal(t, "error", got.IconType)
		assert.Contains(t, got.Message, "Spotify")
		assert.Contains(t, got.Message, "Last.fm")
	})

	t.Run("partial success names failing provider", func(t *testing.T) {
		got := resetSyncNotification(partial, syncErr, nil)
		assert.Equal(t, "Reset Partial", got.Title)
		assert.Equal(t, "warning", got.IconType)
		assert.Contains(t, got.Message, "Spotify")
		assert.False(t, strings.Contains(got.Message, "Navidrome"), "the succeeding provider must not be blamed")
	})

	t.Run("metadata failure reports partial", func(t *testing.T) {
		got := resetSyncNotification(allSucceeded, nil, metaErr)
		assert.Equal(t, "Reset Partial", got.Title)
		assert.Contains(t, got.Message, "metadata")
	})

	t.Run("full success", func(t *testing.T) {
		got := resetSyncNotification(allSucceeded, nil, nil)
		assert.Equal(t, "Reset Complete", got.Title)
		assert.Equal(t, "success", got.IconType)
	})

	t.Run("full success notes backing-off provider", func(t *testing.T) {
		got := resetSyncNotification(backingOff, nil, nil)
		assert.Equal(t, "Reset Complete", got.Title)
		assert.Contains(t, got.Message, "backing off")
		assert.Contains(t, got.Message, "Spotify")
	})
}
