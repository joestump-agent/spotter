package schema_test

// Schema-level validation tests for MusicBrainz ID fields on Artist, Album,
// and Track: well-formed UUIDs (upper or lower case hex) are accepted,
// garbage is rejected, and empty string remains allowed because the fields
// are Optional and unenriched entities legitimately hold empty values.
//
// Also pins ent's validation semantics for legacy rows: validators run only
// for fields present in a mutation, so updating an unrelated field on a row
// whose stored musicbrainz_id is invalid (written before validation existed)
// must not fail.
//
// Governing: AGENTS.md VAL-007 (MusicBrainz IDs MUST be in correct UUID format)

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"spotter/ent"
	"spotter/ent/enttest"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mbidCases is the shared table of musicbrainz_id values and whether writes
// carrying them must be accepted.
var mbidCases = []struct {
	name    string
	mbid    string
	wantErr bool
}{
	{"valid lowercase UUID", "b10bbbfc-cf9e-42e0-be17-e2c3e1d2600d", false},
	{"valid uppercase UUID", "B10BBBFC-CF9E-42E0-BE17-E2C3E1D2600D", false},
	{"valid mixed-case UUID", "b10BBbfc-CF9e-42E0-be17-E2c3e1d2600D", false},
	{"empty string allowed (unenriched)", "", false},
	{"garbage rejected", "not-a-uuid", true},
	{"hex without hyphens rejected", "b10bbbfccf9e42e0be17e2c3e1d2600d", true},
	{"non-hex characters rejected", "g10bbbfc-cf9e-42e0-be17-e2c3e1d2600d", true},
	{"truncated UUID rejected", "b10bbbfc-cf9e-42e0-be17", true},
	{"sentinel value rejected", "mbid-123", true},
}

func openClient(t *testing.T, name string) *ent.Client {
	t.Helper()
	client := enttest.Open(t, "sqlite3",
		fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", name))
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("failed to close ent client: %v", err)
		}
	})
	return client
}

func createUser(t *testing.T, ctx context.Context, client *ent.Client, username string) *ent.User {
	t.Helper()
	u, err := client.User.Create().SetUsername(username).SetTheme("dark").Save(ctx)
	require.NoError(t, err)
	return u
}

// TestArtistMusicbrainzIDValidation verifies UUID-format validation on
// Artist.musicbrainz_id at write time.
// Governing: AGENTS.md VAL-007
func TestArtistMusicbrainzIDValidation(t *testing.T) {
	ctx := context.Background()
	client := openClient(t, "mbid_artist")
	u := createUser(t, ctx, client, "artist-user")

	for i, tc := range mbidCases {
		t.Run(tc.name, func(t *testing.T) {
			art, err := client.Artist.Create().
				SetName(fmt.Sprintf("Artist %d", i)).
				SetUser(u).
				SetMusicbrainzID(tc.mbid).
				Save(ctx)
			if tc.wantErr {
				require.Error(t, err, "invalid MBID %q must be rejected", tc.mbid)
				assert.True(t, ent.IsValidationError(err), "expected a validation error, got: %v", err)
				return
			}
			require.NoError(t, err, "MBID %q must be accepted", tc.mbid)
			assert.Equal(t, tc.mbid, art.MusicbrainzID, "MBID must be stored verbatim (no case normalization)")
		})
	}
}

// TestAlbumMusicbrainzIDValidation verifies UUID-format validation on
// Album.musicbrainz_id at write time.
// Governing: AGENTS.md VAL-007
func TestAlbumMusicbrainzIDValidation(t *testing.T) {
	ctx := context.Background()
	client := openClient(t, "mbid_album")
	u := createUser(t, ctx, client, "album-user")

	for i, tc := range mbidCases {
		t.Run(tc.name, func(t *testing.T) {
			alb, err := client.Album.Create().
				SetName(fmt.Sprintf("Album %d", i)).
				SetUser(u).
				SetMusicbrainzID(tc.mbid).
				Save(ctx)
			if tc.wantErr {
				require.Error(t, err, "invalid MBID %q must be rejected", tc.mbid)
				assert.True(t, ent.IsValidationError(err), "expected a validation error, got: %v", err)
				return
			}
			require.NoError(t, err, "MBID %q must be accepted", tc.mbid)
			assert.Equal(t, tc.mbid, alb.MusicbrainzID, "MBID must be stored verbatim (no case normalization)")
		})
	}
}

// TestTrackMusicbrainzIDValidation verifies UUID-format validation on
// Track.musicbrainz_id at write time.
// Governing: AGENTS.md VAL-007
func TestTrackMusicbrainzIDValidation(t *testing.T) {
	ctx := context.Background()
	client := openClient(t, "mbid_track")

	for i, tc := range mbidCases {
		t.Run(tc.name, func(t *testing.T) {
			tr, err := client.Track.Create().
				SetName(fmt.Sprintf("Track %d", i)).
				SetMusicbrainzID(tc.mbid).
				Save(ctx)
			if tc.wantErr {
				require.Error(t, err, "invalid MBID %q must be rejected", tc.mbid)
				assert.True(t, ent.IsValidationError(err), "expected a validation error, got: %v", err)
				return
			}
			require.NoError(t, err, "MBID %q must be accepted", tc.mbid)
			require.NotNil(t, tr.MusicbrainzID)
			assert.Equal(t, tc.mbid, *tr.MusicbrainzID, "MBID must be stored verbatim (no case normalization)")
		})
	}
}

// TestMusicbrainzIDValidation_LegacyInvalidRowUpdatableOnOtherFields pins the
// migration/dirty-data behavior: validators apply on WRITE only and ent only
// validates fields present in the mutation. A row whose musicbrainz_id was
// written before validation existed (and is invalid) must remain updatable on
// OTHER fields; only a mutation that touches musicbrainz_id itself re-triggers
// validation.
// Governing: AGENTS.md VAL-007
func TestMusicbrainzIDValidation_LegacyInvalidRowUpdatableOnOtherFields(t *testing.T) {
	ctx := context.Background()
	dsn := "file:mbid_legacy?mode=memory&cache=shared&_fk=1"
	client := enttest.Open(t, "sqlite3", dsn)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("failed to close ent client: %v", err)
		}
	})

	u := createUser(t, ctx, client, "legacy-user")
	art, err := client.Artist.Create().SetName("Legacy Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)

	// Plant an invalid-legacy MBID behind ent's back via raw SQL, simulating a
	// row written before UUID validation existed. Same shared-cache in-memory
	// database as the ent client.
	db, err := sql.Open("sqlite3", dsn)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("failed to close sql db: %v", err)
		}
	})
	res, err := db.ExecContext(ctx,
		"UPDATE artists SET musicbrainz_id = ? WHERE id = ?", "legacy-not-a-uuid", art.ID)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "legacy row must have been planted")

	// Updating an unrelated field must succeed: musicbrainz_id is not part of
	// this mutation, so its validator must not run against the stored value.
	_, err = client.Artist.UpdateOneID(art.ID).SetBio("updated bio").Save(ctx)
	require.NoError(t, err,
		"updating an unrelated field on a row with an invalid-legacy musicbrainz_id must not fail")

	got, err := client.Artist.Get(ctx, art.ID)
	require.NoError(t, err)
	assert.Equal(t, "updated bio", got.Bio)
	assert.Equal(t, "legacy-not-a-uuid", got.MusicbrainzID,
		"legacy value must be left untouched by unrelated updates")

	// But a mutation that DOES touch musicbrainz_id re-triggers validation.
	err = client.Artist.UpdateOneID(art.ID).SetMusicbrainzID("still-not-a-uuid").Exec(ctx)
	require.Error(t, err, "writing an invalid MBID must still be rejected")
	assert.True(t, ent.IsValidationError(err), "expected a validation error, got: %v", err)
}
