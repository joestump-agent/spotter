// White-box tests for the weekly chart bucketing in getProviderHistory (issue #13):
// data keys and axis labels must use the same canonical week-start so weekly
// charts are never all zeros.
package handlers

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"spotter/ent/enttest"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartOfWeek(t *testing.T) {
	tests := []struct {
		name     string
		input    time.Time
		expected time.Time
	}{
		{
			name:     "thursday new year maps to monday of previous year",
			input:    time.Date(2026, 1, 1, 15, 30, 0, 0, time.UTC), // Thursday
			expected: time.Date(2025, 12, 29, 0, 0, 0, 0, time.UTC), // Monday
		},
		{
			name:     "sunday maps back to monday across year boundary",
			input:    time.Date(2026, 1, 4, 23, 59, 59, 0, time.UTC), // Sunday
			expected: time.Date(2025, 12, 29, 0, 0, 0, 0, time.UTC),  // Monday
		},
		{
			name:     "tuesday december 31 stays in same year",
			input:    time.Date(2024, 12, 31, 8, 0, 0, 0, time.UTC), // Tuesday
			expected: time.Date(2024, 12, 30, 0, 0, 0, 0, time.UTC), // Monday
		},
		{
			name:     "monday is idempotent",
			input:    time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC), // Monday
			expected: time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC),
		},
		{
			name:     "monday with time-of-day truncates to midnight",
			input:    time.Date(2026, 7, 6, 18, 45, 12, 0, time.UTC),
			expected: time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := startOfWeek(tt.input)
			assert.True(t, got.Equal(tt.expected), "startOfWeek(%v) = %v, want %v", tt.input, got, tt.expected)
			assert.Equal(t, time.Monday, got.Weekday())
		})
	}
}

// TestStartOfWeek_KeysAlignWithLabels verifies the alignment invariant directly:
// stepping from startOfWeek(anchor) in 7-day increments always lands exactly on
// startOfWeek of any date inside those weeks, including across a year boundary.
func TestStartOfWeek_KeysAlignWithLabels(t *testing.T) {
	anchor := time.Date(2025, 11, 20, 10, 0, 0, 0, time.UTC)
	labelStarts := make(map[string]bool)
	current := startOfWeek(anchor)
	for i := 0; i < 20; i++ { // ~4.5 months, spans into 2026
		labelStarts[current.Format("Jan 2")] = true
		current = current.AddDate(0, 0, 7)
	}

	// Every day within the covered range must key into one of the label buckets.
	for d := anchor; d.Before(current); d = d.AddDate(0, 0, 1) {
		key := startOfWeek(d).Format("Jan 2")
		assert.True(t, labelStarts[key], "day %v produced key %q with no matching label", d, key)
	}
}

func TestGetProviderHistory_WeeklyTimeframeBucketsListens(t *testing.T) {
	dbName := strings.NewReplacer("/", "_", " ", "_", "=", "_").Replace(t.Name())
	client := enttest.Open(t, "sqlite3", "file:"+dbName+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	h := &Handler{
		Client: client,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	ctx := context.Background()
	u, err := client.User.Create().
		SetUsername("weekbuckets_user").
		SetPaginationSize(25).
		Save(ctx)
	require.NoError(t, err)

	// Spread listens across the 6-month window, including different weekdays.
	now := time.Now()
	playedAts := []time.Time{
		now.AddDate(0, 0, -3),
		now.AddDate(0, 0, -10),
		now.AddDate(0, 0, -45),
		now.AddDate(0, 0, -100),
		now.AddDate(0, 0, -150),
	}
	for i, playedAt := range playedAts {
		_, err := client.Listen.Create().
			SetUser(u).
			SetTrackName("Track " + string(rune('A'+i))).
			SetArtistName("Chart Artist").
			SetAlbumName("Chart Album").
			SetSource("spotify").
			SetPlayedAt(playedAt).
			Save(ctx)
		require.NoError(t, err)
	}

	data := h.getProviderHistory(ctx, u.ID, "Chart Artist", "", "", timeframe6m)

	require.Len(t, data.Datasets, 1, "expected a single provider dataset")
	require.NotEmpty(t, data.Labels)

	// Issue #13: keys built from ISOWeek-projected dates never matched the
	// startDate+n*7d labels, so every data point rendered as zero. With the
	// canonical week-start used for both, every listen lands in a label bucket.
	total := 0.0
	for _, v := range data.Datasets[0].Data {
		total += v
	}
	assert.Equal(t, float64(len(playedAts)), total,
		"all listens must be counted in the weekly buckets (labels: %v, data: %v)",
		data.Labels, data.Datasets[0].Data)
}
