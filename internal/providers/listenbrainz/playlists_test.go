// Governing: SPEC music-provider-integration REQ "ListenBrainz Provider" (REQ-PROV-049)
package listenbrainz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"spotter/internal/config"
	"spotter/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	userPlaylistMBID = "11111111-2222-3333-4444-555555555555"
	jamsPlaylistMBID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	recordingMBID1   = "2cfad207-3f55-4aec-8120-86cf66e34d59"
	recordingMBID2   = "97e69767-5d34-4c97-b36a-f3b2b1ef9dae"
)

// playlistStubJSON builds a JSPF playlist stub as served by the listing
// endpoints (no track array).
func playlistStubJSON(title, mbid string) string {
	return fmt.Sprintf(`{"playlist": {
		"title": %q,
		"creator": "lb-user",
		"identifier": "https://listenbrainz.org/playlist/%s"
	}}`, title, mbid)
}

// listBodyJSON wraps stubs in the playlist listing response envelope.
func listBodyJSON(total, offset int, stubs ...string) string {
	joined := ""
	for i, s := range stubs {
		if i > 0 {
			joined += ","
		}
		joined += s
	}
	return fmt.Sprintf(`{"playlists": [%s], "playlist_count": %d, "count": %d, "offset": %d}`,
		joined, total, len(stubs), offset)
}

// newPlaylistServer serves the three playlist endpoints with the given
// bodies. fullBodies maps playlist MBID -> full JSPF response body.
func newPlaylistServer(t *testing.T, userBody, createdForBody string, fullBodies map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Token user-token-123", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/1/user/lb-user/playlists":
			fmt.Fprint(w, userBody)
		case "/1/user/lb-user/playlists/createdfor":
			fmt.Fprint(w, createdForBody)
		default:
			mbid := r.URL.Path[len("/1/playlist/"):]
			body, ok := fullBodies[mbid]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"code": 404, "error": "not found"}`)
				return
			}
			fmt.Fprint(w, body)
		}
	}))
}

// Governing: SPEC music-provider-integration REQ-PROV-049 (user playlists +
// createdfor fetched, JSPF parsed, recording MBIDs extracted from identifier
// URIs — list form)
func TestGetPlaylists(t *testing.T) {
	fullUser := fmt.Sprintf(`{"playlist": {
		"title": "My Mix",
		"creator": "lb-user",
		"annotation": "<p>hand-picked <em>favourites</em></p>",
		"identifier": "https://listenbrainz.org/playlist/%s",
		"track": [
			{
				"title": "Song One",
				"creator": "Artist A",
				"album": "Album X",
				"duration": 215000,
				"identifier": ["https://musicbrainz.org/recording/%s"]
			},
			{
				"title": "Song Two",
				"creator": "Artist B",
				"album": "Album Y",
				"duration": 180000,
				"identifier": ["https://musicbrainz.org/recording/%s"]
			}
		]
	}}`, userPlaylistMBID, recordingMBID1, recordingMBID2)

	// Generated playlist (created FOR the user by troi-bot): the createdfor
	// endpoint is what surfaces Weekly Jams and friends.
	fullJams := fmt.Sprintf(`{"playlist": {
		"title": "Weekly Jams for lb-user, week of 2026-07-06",
		"creator": "troi-bot",
		"annotation": "<p>Your weekly jams!</p>",
		"identifier": "https://listenbrainz.org/playlist/%s",
		"track": [
			{
				"title": "Jam Track",
				"creator": "Artist A",
				"album": "Album X",
				"duration": 200000,
				"identifier": ["https://musicbrainz.org/recording/%s"]
			}
		]
	}}`, jamsPlaylistMBID, recordingMBID1)

	server := newPlaylistServer(t,
		listBodyJSON(1, 0, playlistStubJSON("My Mix", userPlaylistMBID)),
		listBodyJSON(1, 0, playlistStubJSON("Weekly Jams", jamsPlaylistMBID)),
		map[string]string{userPlaylistMBID: fullUser, jamsPlaylistMBID: fullJams},
	)
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)
	playlists, err := p.GetPlaylists(context.Background())
	require.NoError(t, err)
	require.Len(t, playlists, 2)

	mix := playlists[0]
	assert.Equal(t, userPlaylistMBID, mix.ID, "playlist MBID is the remote ID")
	assert.Equal(t, "My Mix", mix.Name)
	assert.Equal(t, "hand-picked favourites", mix.Description, "annotation HTML is stripped")
	assert.Equal(t, "https://listenbrainz.org/playlist/"+userPlaylistMBID, mix.ExternalURL)
	assert.Equal(t, 2, mix.TrackCount)
	assert.Equal(t, 2, mix.UniqueArtists)
	assert.Equal(t, 2, mix.UniqueAlbums)
	require.Len(t, mix.Tracks, 2)
	assert.Equal(t, providers.Track{
		ID:         recordingMBID1,
		Name:       "Song One",
		Artist:     "Artist A",
		Album:      "Album X",
		DurationMs: 215000,
		URL:        "https://musicbrainz.org/recording/" + recordingMBID1,
	}, mix.Tracks[0])
	assert.Equal(t, recordingMBID2, mix.Tracks[1].ID)

	jams := playlists[1]
	assert.Equal(t, jamsPlaylistMBID, jams.ID)
	assert.Equal(t, "Weekly Jams for lb-user, week of 2026-07-06", jams.Name)
	require.Len(t, jams.Tracks, 1)
	assert.Equal(t, recordingMBID1, jams.Tracks[0].ID)
}

// Governing: SPEC music-provider-integration REQ-PROV-049 (identifier accepted
// in both JSPF encodings: single URI string and list of URI strings)
func TestGetPlaylists_IdentifierStringForm(t *testing.T) {
	// The JSPF spec defines identifier as a single URI string; ListenBrainz
	// emits a list. Both forms must parse.
	full := fmt.Sprintf(`{"playlist": {
		"title": "String IDs",
		"identifier": "https://listenbrainz.org/playlist/%s",
		"track": [
			{
				"title": "Spec Form",
				"creator": "Artist A",
				"identifier": "https://musicbrainz.org/recording/%s"
			},
			{
				"title": "No MBID",
				"creator": "Artist B",
				"identifier": ["https://example.com/something-else"]
			}
		]
	}}`, userPlaylistMBID, recordingMBID1)

	server := newPlaylistServer(t,
		listBodyJSON(1, 0, playlistStubJSON("String IDs", userPlaylistMBID)),
		listBodyJSON(0, 0),
		map[string]string{userPlaylistMBID: full},
	)
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)
	playlists, err := p.GetPlaylists(context.Background())
	require.NoError(t, err)
	require.Len(t, playlists, 1)
	require.Len(t, playlists[0].Tracks, 2)

	assert.Equal(t, recordingMBID1, playlists[0].Tracks[0].ID, "string-form identifier yields the MBID")
	// A track without a recording identifier is still delivered for
	// name/artist matching (ADR-0014 tiers 2/3), just without a remote ID.
	assert.Equal(t, "", playlists[0].Tracks[1].ID)
	assert.Equal(t, "No MBID", playlists[0].Tracks[1].Name)
}

// Governing: SPEC music-provider-integration REQ-PROV-049 (count/offset pagination)
func TestGetPlaylists_Pagination(t *testing.T) {
	total := playlistsPageSize + 3 // forces a second page
	stubs := make([]string, total)
	fullBodies := make(map[string]string, total)
	for i := 0; i < total; i++ {
		mbid := fmt.Sprintf("%08d-0000-4000-8000-000000000000", i)
		stubs[i] = playlistStubJSON(fmt.Sprintf("Playlist %d", i), mbid)
		fullBodies[mbid] = fmt.Sprintf(`{"playlist": {
			"title": "Playlist %d",
			"identifier": "https://listenbrainz.org/playlist/%s",
			"track": []
		}}`, i, mbid)
	}

	var listRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/1/user/lb-user/playlists":
			listRequests++
			assert.Equal(t, strconv.Itoa(playlistsPageSize), r.URL.Query().Get("count"))
			offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
			require.NoError(t, err)
			end := offset + playlistsPageSize
			if end > total {
				end = total
			}
			fmt.Fprint(w, listBodyJSON(total, offset, stubs[offset:end]...))
		case "/1/user/lb-user/playlists/createdfor":
			fmt.Fprint(w, listBodyJSON(0, 0))
		default:
			fmt.Fprint(w, fullBodies[r.URL.Path[len("/1/playlist/"):]])
		}
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)
	playlists, err := p.GetPlaylists(context.Background())
	require.NoError(t, err)
	assert.Len(t, playlists, total)
	assert.Equal(t, 2, listRequests, "expected exactly two pages")
}

func TestGetPlaylists_Empty(t *testing.T) {
	server := newPlaylistServer(t, listBodyJSON(0, 0), listBodyJSON(0, 0), nil)
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)
	playlists, err := p.GetPlaylists(context.Background())
	require.NoError(t, err)
	assert.Empty(t, playlists)
}

// Governing: SPEC error-handling REQ-ERR-003 (unparseable response body is fatal)
func TestGetPlaylists_MalformedJSPF(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"invalid json", `{"playlists": [`},
		{"identifier is a number", `{"playlists": [{"playlist": {"title": "Bad", "identifier": 42}}], "playlist_count": 1, "count": 1, "offset": 0}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()

			p := createTestProvider(t, &config.Config{}, testUser(), server.URL)
			_, err := p.GetPlaylists(context.Background())
			require.Error(t, err)
			assert.ErrorIs(t, err, providers.ErrMalformedResponse)
		})
	}
}

// Governing: SPEC music-provider-integration REQ-PROV-047 (429 honored via Retry-After)
func TestGetPlaylists_RateLimited(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/1/user/lb-user/playlists":
			attempts++
			if attempts == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			fmt.Fprint(w, listBodyJSON(0, 0))
		case "/1/user/lb-user/playlists/createdfor":
			fmt.Fprint(w, listBodyJSON(0, 0))
		}
	}))
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)
	playlists, err := p.GetPlaylists(context.Background())
	require.NoError(t, err)
	assert.Empty(t, playlists)
	assert.Equal(t, 2, attempts, "429 must be retried after the advertised interval")
}

// A playlist whose full fetch fails is kept as a trackless stub so the sync
// reconciler does not deactivate it and existing tracks are left untouched.
func TestGetPlaylists_FullFetchFailureKeepsStub(t *testing.T) {
	// fullBodies is empty, so the /1/playlist/{mbid} fetch 404s.
	server := newPlaylistServer(t,
		listBodyJSON(1, 0, playlistStubJSON("Unfetchable", userPlaylistMBID)),
		listBodyJSON(0, 0),
		nil,
	)
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)
	playlists, err := p.GetPlaylists(context.Background())
	require.NoError(t, err)
	require.Len(t, playlists, 1)
	assert.Equal(t, userPlaylistMBID, playlists[0].ID)
	assert.Equal(t, "Unfetchable", playlists[0].Name)
	assert.Empty(t, playlists[0].Tracks)
}

// A playlist returned by both listing endpoints is imported once (the
// playlist MBID is the upsert key downstream).
func TestGetPlaylists_DeduplicatesAcrossEndpoints(t *testing.T) {
	full := fmt.Sprintf(`{"playlist": {
		"title": "Both Lists",
		"identifier": "https://listenbrainz.org/playlist/%s",
		"track": []
	}}`, userPlaylistMBID)

	stub := playlistStubJSON("Both Lists", userPlaylistMBID)
	server := newPlaylistServer(t,
		listBodyJSON(1, 0, stub),
		listBodyJSON(1, 0, stub),
		map[string]string{userPlaylistMBID: full},
	)
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)
	playlists, err := p.GetPlaylists(context.Background())
	require.NoError(t, err)
	assert.Len(t, playlists, 1)
}

// Stubs without a parseable playlist MBID cannot be fetched or upserted and
// are skipped; tracks without title/creator are skipped within a playlist.
func TestGetPlaylists_SkipsUnusableEntries(t *testing.T) {
	full := fmt.Sprintf(`{"playlist": {
		"title": "Partial",
		"identifier": "https://listenbrainz.org/playlist/%s",
		"track": [
			{"title": "", "creator": "Artist A"},
			{"title": "Nameless Artist", "creator": ""},
			{"title": "Keeper", "creator": "Artist A",
			 "identifier": ["https://musicbrainz.org/recording/%s"]}
		]
	}}`, userPlaylistMBID, recordingMBID1)

	noMBIDStub := `{"playlist": {"title": "No MBID", "identifier": "https://listenbrainz.org/playlist/not-a-uuid"}}`
	server := newPlaylistServer(t,
		listBodyJSON(2, 0, noMBIDStub, playlistStubJSON("Partial", userPlaylistMBID)),
		listBodyJSON(0, 0),
		map[string]string{userPlaylistMBID: full},
	)
	defer server.Close()

	p := createTestProvider(t, &config.Config{}, testUser(), server.URL)
	playlists, err := p.GetPlaylists(context.Background())
	require.NoError(t, err)
	require.Len(t, playlists, 1)
	require.Len(t, playlists[0].Tracks, 1)
	assert.Equal(t, "Keeper", playlists[0].Tracks[0].Name)
	assert.Equal(t, 1, playlists[0].TrackCount, "track count reflects usable tracks")
}

// Governing: SPEC music-provider-integration REQ-PROV-049 (read-only: playlist
// writes fail with ErrPlaylistWriteNotSupported)
func TestCreatePlaylist_NotSupported(t *testing.T) {
	p := createTestProvider(t, &config.Config{}, testUser(), "http://unreachable.invalid")
	err := p.CreatePlaylist(context.Background(), "name", "desc", []providers.Track{{Name: "t", Artist: "a"}})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPlaylistWriteNotSupported))
}

// Governing: SPEC music-provider-integration REQ-PROV-049 (MBID extracted from
// http(s)://musicbrainz.org/recording/<mbid> identifier URIs only)
func TestRecordingMBIDFromURI(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want string
	}{
		{"https form", "https://musicbrainz.org/recording/" + recordingMBID1, recordingMBID1},
		{"http form", "http://musicbrainz.org/recording/" + recordingMBID1, recordingMBID1},
		{"trailing slash", "https://musicbrainz.org/recording/" + recordingMBID1 + "/", recordingMBID1},
		{"not a recording uri", "https://listenbrainz.org/playlist/" + recordingMBID1, ""},
		{"invalid mbid", "https://musicbrainz.org/recording/not-a-uuid", ""},
		{"embedded in another host's path", "https://evil.example/musicbrainz.org/recording/" + recordingMBID1, ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, recordingMBIDFromURI(tt.uri))
		})
	}
}

func TestPlaylistMBID(t *testing.T) {
	tests := []struct {
		name string
		ids  jspfIdentifier
		want string
	}{
		{"listenbrainz url", jspfIdentifier{"https://listenbrainz.org/playlist/" + userPlaylistMBID}, userPlaylistMBID},
		{"trailing slash", jspfIdentifier{"https://listenbrainz.org/playlist/" + userPlaylistMBID + "/"}, userPlaylistMBID},
		{"invalid uuid", jspfIdentifier{"https://listenbrainz.org/playlist/oops"}, ""},
		{"not a playlist url", jspfIdentifier{"https://musicbrainz.org/recording/" + recordingMBID1}, ""},
		{"second identifier wins", jspfIdentifier{"https://example.com/x", "https://listenbrainz.org/playlist/" + userPlaylistMBID}, userPlaylistMBID},
		{"empty", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, playlistMBID(tt.ids))
		})
	}
}

func TestJSPFIdentifier_Unmarshal(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		want    jspfIdentifier
		wantErr bool
	}{
		{"string", `"https://a"`, jspfIdentifier{"https://a"}, false},
		{"empty string", `""`, nil, false},
		{"list", `["https://a", "https://b"]`, jspfIdentifier{"https://a", "https://b"}, false},
		{"empty list", `[]`, jspfIdentifier{}, false},
		{"number is malformed", `42`, nil, true},
		{"object is malformed", `{"uri": "https://a"}`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got jspfIdentifier
			err := json.Unmarshal([]byte(tt.json), &got)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestStripHTML(t *testing.T) {
	assert.Equal(t, "Your weekly jams!", stripHTML("<p>Your <b>weekly</b> jams!</p>"))
	assert.Equal(t, "plain", stripHTML("plain"))
	assert.Equal(t, "", stripHTML(""))
}
