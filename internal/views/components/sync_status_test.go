package components

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestGetSyncState_AllFiveStates tests the 5-state sync logic
func TestGetSyncState_AllFiveStates(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		config   SyncStatusConfig
		expected SyncState
	}{
		// State 1: Neutral - Sync Disabled
		{
			name: "Neutral - sync disabled",
			config: SyncStatusConfig{
				SyncEnabled:   false,
				NavidromeID:   "abc123",
				TotalTracks:   10,
				MatchedTracks: 10,
			},
			expected: SyncStateNeutral,
		},
		{
			name: "Neutral - sync disabled even with partial match",
			config: SyncStatusConfig{
				SyncEnabled:   false,
				NavidromeID:   "abc123",
				TotalTracks:   10,
				MatchedTracks: 5,
			},
			expected: SyncStateNeutral,
		},

		// State 2: Error - SyncError is set
		{
			name: "Error - sync error present",
			config: SyncStatusConfig{
				SyncEnabled:   true,
				NavidromeID:   "abc123",
				SyncError:     "connection failed",
				TotalTracks:   10,
				MatchedTracks: 10,
			},
			expected: SyncStateError,
		},
		{
			name: "Error - sync error takes priority over pending",
			config: SyncStatusConfig{
				SyncEnabled: true,
				NavidromeID: "", // would be pending without error
				SyncError:   "initialization failed",
			},
			expected: SyncStateError,
		},

		// State 3: Pending - Sync enabled but no NavidromeID yet
		{
			name: "Pending - sync enabled, no NavidromeID",
			config: SyncStatusConfig{
				SyncEnabled:   true,
				NavidromeID:   "",
				TotalTracks:   10,
				MatchedTracks: 0,
			},
			expected: SyncStatePending,
		},
		{
			name: "Pending - initial sync in progress",
			config: SyncStatusConfig{
				SyncEnabled:    true,
				NavidromeID:    "",
				LastSyncedAt:   nil,
				TotalTracks:    25,
				MatchedTracks:  0,
				TargetProvider: "navidrome",
			},
			expected: SyncStatePending,
		},

		// State 4: Neutral - No tracks to sync (TotalTracks == 0)
		{
			name: "Neutral - zero total tracks",
			config: SyncStatusConfig{
				SyncEnabled:   true,
				NavidromeID:   "abc123",
				TotalTracks:   0,
				MatchedTracks: 0,
			},
			expected: SyncStateNeutral,
		},
		{
			name: "Neutral - empty playlist after sync",
			config: SyncStatusConfig{
				SyncEnabled:  true,
				NavidromeID:  "xyz789",
				LastSyncedAt: &now,
				TotalTracks:  0,
			},
			expected: SyncStateNeutral,
		},

		// State 5: Success - All tracks matched
		{
			name: "Success - 100% match rate",
			config: SyncStatusConfig{
				SyncEnabled:   true,
				NavidromeID:   "abc123",
				TotalTracks:   10,
				MatchedTracks: 10,
			},
			expected: SyncStateSuccess,
		},
		{
			name: "Success - single track fully matched",
			config: SyncStatusConfig{
				SyncEnabled:   true,
				NavidromeID:   "abc123",
				TotalTracks:   1,
				MatchedTracks: 1,
			},
			expected: SyncStateSuccess,
		},
		{
			name: "Success - large playlist fully synced",
			config: SyncStatusConfig{
				SyncEnabled:   true,
				NavidromeID:   "abc123",
				TotalTracks:   500,
				MatchedTracks: 500,
				LastSyncedAt:  &now,
			},
			expected: SyncStateSuccess,
		},

		// State 6: Warning - Partial match (MatchedTracks < TotalTracks)
		{
			name: "Warning - partial match (some tracks)",
			config: SyncStatusConfig{
				SyncEnabled:   true,
				NavidromeID:   "abc123",
				TotalTracks:   10,
				MatchedTracks: 5,
			},
			expected: SyncStateWarning,
		},
		{
			name: "Warning - partial match (one track missing)",
			config: SyncStatusConfig{
				SyncEnabled:   true,
				NavidromeID:   "abc123",
				TotalTracks:   10,
				MatchedTracks: 9,
			},
			expected: SyncStateWarning,
		},
		// CRITICAL: The "0 matches" bug fix test case
		{
			name: "Warning - ZERO matches (not success!) - the False Success bug",
			config: SyncStatusConfig{
				SyncEnabled:   true,
				NavidromeID:   "abc123", // Sync completed (has NavidromeID)
				TotalTracks:   10,
				MatchedTracks: 0, // No tracks matched - should be WARNING not Success
			},
			expected: SyncStateWarning,
		},
		{
			name: "Warning - zero matches on large playlist",
			config: SyncStatusConfig{
				SyncEnabled:   true,
				NavidromeID:   "def456",
				TotalTracks:   100,
				MatchedTracks: 0,
				LastSyncedAt:  &now,
			},
			expected: SyncStateWarning,
		},
		{
			name: "Warning - single track unmatched",
			config: SyncStatusConfig{
				SyncEnabled:   true,
				NavidromeID:   "ghi789",
				TotalTracks:   1,
				MatchedTracks: 0,
			},
			expected: SyncStateWarning,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getSyncState(tt.config)
			assert.Equal(t, tt.expected, result, "getSyncState returned wrong state")
		})
	}
}

// TestGetSyncState_ZeroMatchesBugRegression specifically tests the "False Success" bug
// where playlists with 0 matched tracks were incorrectly showing as Success (green)
func TestGetSyncState_ZeroMatchesBugRegression(t *testing.T) {
	// This was the bug: A playlist with sync enabled, a valid NavidromeID (meaning sync
	// completed), but 0 matched tracks was showing as Success (green) instead of Warning (orange)

	config := SyncStatusConfig{
		EntityType:     "playlist",
		EntityID:       123,
		SyncEnabled:    true,
		NavidromeID:    "navidrome-playlist-id", // Sync has completed
		TotalTracks:    30,
		MatchedTracks:  0, // But no tracks were found in Navidrome!
		TargetProvider: "navidrome",
	}

	state := getSyncState(config)

	// The fix: 0 matches should be WARNING, not SUCCESS
	assert.Equal(t, SyncStateWarning, state,
		"Zero matched tracks should return Warning state, not Success")
	assert.NotEqual(t, SyncStateSuccess, state,
		"Zero matched tracks must NOT return Success state (this was the bug)")
	assert.NotEqual(t, SyncStateNeutral, state,
		"Zero matched tracks should not be Neutral when there are tracks to sync")
}

// TestGetSyncStateLabel tests the human-readable labels for each state
func TestGetSyncStateLabel(t *testing.T) {
	tests := []struct {
		name     string
		state    SyncState
		config   SyncStatusConfig
		expected string
	}{
		{
			name:     "Success label",
			state:    SyncStateSuccess,
			config:   SyncStatusConfig{SyncEnabled: true},
			expected: "Fully Synced",
		},
		{
			name:     "Warning label",
			state:    SyncStateWarning,
			config:   SyncStatusConfig{SyncEnabled: true},
			expected: "Partial Sync",
		},
		{
			name:     "Error label",
			state:    SyncStateError,
			config:   SyncStatusConfig{SyncEnabled: true},
			expected: "Sync Error",
		},
		{
			name:     "Pending label",
			state:    SyncStatePending,
			config:   SyncStatusConfig{SyncEnabled: true},
			expected: "Syncing...",
		},
		{
			name:     "Neutral label - sync disabled",
			state:    SyncStateNeutral,
			config:   SyncStatusConfig{SyncEnabled: false},
			expected: "Sync Disabled",
		},
		{
			name:     "Neutral label - sync enabled (no tracks)",
			state:    SyncStateNeutral,
			config:   SyncStatusConfig{SyncEnabled: true},
			expected: "Not Synced",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getSyncStateLabel(tt.state, tt.config)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestGetBadgeClass tests the badge CSS class assignment
func TestGetBadgeClass(t *testing.T) {
	tests := []struct {
		state    SyncState
		expected string
	}{
		{SyncStateSuccess, "badge-success"},
		{SyncStateWarning, "badge-warning"},
		{SyncStateError, "badge-error"},
		{SyncStatePending, "badge-info"},
		{SyncStateNeutral, "badge-ghost"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := getBadgeClass(tt.state)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestGetProgressBarClass tests the progress bar CSS class assignment
func TestGetProgressBarClass(t *testing.T) {
	tests := []struct {
		state    SyncState
		expected string
	}{
		{SyncStateSuccess, "progress-success"},
		{SyncStateWarning, "progress-warning"},
		{SyncStateError, "progress-error"},
		{SyncStatePending, "progress-info"},
		{SyncStateNeutral, "progress-info"}, // Neutral defaults to info
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := getProgressBarClass(tt.state)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestGetButtonClass tests the button CSS class assignment
func TestGetButtonClass(t *testing.T) {
	tests := []struct {
		state    SyncState
		expected string
	}{
		{SyncStateSuccess, "btn-success"},
		{SyncStateWarning, "btn-warning"},
		{SyncStateError, "btn-error"},
		{SyncStatePending, "btn-info"},
		{SyncStateNeutral, "btn-ghost"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := getButtonClass(tt.state)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestIsPending tests the isPending helper function
func TestIsPending(t *testing.T) {
	tests := []struct {
		name     string
		config   SyncStatusConfig
		expected bool
	}{
		{
			name: "Pending - sync enabled, no NavidromeID",
			config: SyncStatusConfig{
				SyncEnabled: true,
				NavidromeID: "",
				SyncError:   "",
			},
			expected: true,
		},
		{
			name: "Not pending - sync disabled",
			config: SyncStatusConfig{
				SyncEnabled: false,
				NavidromeID: "",
			},
			expected: false,
		},
		{
			name: "Not pending - has NavidromeID",
			config: SyncStatusConfig{
				SyncEnabled: true,
				NavidromeID: "abc123",
			},
			expected: false,
		},
		{
			name: "Not pending - has error",
			config: SyncStatusConfig{
				SyncEnabled: true,
				NavidromeID: "",
				SyncError:   "some error",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPending(tt.config)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestGetMatchPercentage tests the match percentage calculation
func TestGetMatchPercentage(t *testing.T) {
	tests := []struct {
		matched  int
		total    int
		expected int
	}{
		{10, 10, 100},
		{5, 10, 50},
		{0, 10, 0},
		{0, 0, 0},  // Edge case: empty playlist
		{1, 3, 33}, // Rounds down
		{2, 3, 66}, // Rounds down
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			result := getMatchPercentage(tt.matched, tt.total)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestBuildPlaylistSyncDropdownConfig_MenuHeaderAndFooter tests that the MenuHeader
// and MenuFooter fields are correctly populated based on sync state
func TestBuildPlaylistSyncDropdownConfig_MenuHeaderAndFooter(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name             string
		config           PlaylistSyncDropdownConfig
		expectMenuHeader bool
		expectMenuFooter bool
	}{
		{
			name: "MenuHeader populated when sync enabled",
			config: PlaylistSyncDropdownConfig{
				PlaylistID:      123,
				PlaylistName:    "Test Playlist",
				Source:          "spotify",
				SyncToNavidrome: true,
				NavidromeID:     "nav-123",
				LastSyncedAt:    &now,
				MatchedTracks:   8,
				TotalTracks:     10,
			},
			expectMenuHeader: true,
			expectMenuFooter: true,
		},
		{
			name: "MenuHeader populated when sync enabled with error",
			config: PlaylistSyncDropdownConfig{
				PlaylistID:      124,
				PlaylistName:    "Error Playlist",
				Source:          "lastfm",
				SyncToNavidrome: true,
				NavidromeID:     "nav-124",
				LastSyncedAt:    &now,
				SyncError:       "Connection failed",
				MatchedTracks:   0,
				TotalTracks:     5,
			},
			expectMenuHeader: true,
			expectMenuFooter: true,
		},
		{
			name: "MenuHeader populated when sync enabled but pending",
			config: PlaylistSyncDropdownConfig{
				PlaylistID:      125,
				PlaylistName:    "Pending Playlist",
				Source:          "spotify",
				SyncToNavidrome: true,
				NavidromeID:     "", // No NavidromeID yet - pending
				LastSyncedAt:    nil,
				MatchedTracks:   0,
				TotalTracks:     15,
			},
			expectMenuHeader: true,
			expectMenuFooter: true,
		},
		{
			name: "MenuHeader nil when sync disabled",
			config: PlaylistSyncDropdownConfig{
				PlaylistID:      126,
				PlaylistName:    "Disabled Playlist",
				Source:          "spotify",
				SyncToNavidrome: false,
				NavidromeID:     "",
				LastSyncedAt:    nil,
				MatchedTracks:   0,
				TotalTracks:     20,
			},
			expectMenuHeader: false,
			expectMenuFooter: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildPlaylistSyncDropdownConfig(tt.config)

			if tt.expectMenuHeader {
				assert.NotNil(t, result.MenuHeader, "MenuHeader should be populated when sync is enabled")
			} else {
				assert.Nil(t, result.MenuHeader, "MenuHeader should be nil when sync is disabled")
			}

			if tt.expectMenuFooter {
				assert.NotNil(t, result.MenuFooter, "MenuFooter should be populated when sync is enabled")
			} else {
				assert.Nil(t, result.MenuFooter, "MenuFooter should be nil when sync is disabled")
			}

			// Verify MenuWidth is set for enabled sync
			if tt.config.SyncToNavidrome {
				assert.Equal(t, "w-64", result.MenuWidth, "MenuWidth should be w-64 for playlist sync dropdown")
			}

			// Verify other fields are still set correctly
			assert.Equal(t, fmt.Sprintf("playlist-sync-%d", tt.config.PlaylistID), result.ID)
			assert.Equal(t, tt.config.SyncToNavidrome, result.IsEnabled)
			assert.Equal(t, "sm", result.Size)

			// Verify state-based styling
			syncConfig := toSyncStatusConfig(tt.config)
			state := getSyncState(syncConfig)
			expectedButtonClass := getButtonClass(state)
			assert.Equal(t, expectedButtonClass, result.ButtonClass)
		})
	}
}
