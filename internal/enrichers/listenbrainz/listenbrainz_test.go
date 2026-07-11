package listenbrainz

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"spotter/ent"
	"spotter/internal/config"
	"spotter/internal/enrichers"
	"spotter/internal/httputil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testArtistMBID    = "db92a151-1ac2-438b-bc43-b82e149ddd50"
	testRecordingMBID = "b1a9c0e9-d987-4042-ae91-78d6a3267d69"
)

func strPtr(s string) *string { return &s }

func int64Ptr(i int64) *int64 { return &i }

// createTestEnricher builds a ListenBrainz enricher pointed at a test server.
func createTestEnricher(t *testing.T, serverURL string) *Enricher {
	t.Helper()

	cfg := &config.Config{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	factory := New(logger, cfg)
	enricher, err := factory(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, enricher)

	lb, ok := enricher.(*Enricher)
	require.True(t, ok)
	return lb.WithBaseURL(serverURL)
}

// failServer returns a test server that fails the test if any request lands.
func failServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s", r.URL.Path)
	}))
}

// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-060 (token-less availability)
func TestNew_TokenlessAlwaysAvailable(t *testing.T) {
	cfg := &config.Config{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	factory := New(logger, cfg)
	// nil user: public endpoints need no ListenBrainzAuth
	enricher, err := factory(context.Background(), nil)

	require.NoError(t, err)
	require.NotNil(t, enricher)

	assert.Equal(t, enrichers.TypeListenBrainz, enricher.Type())
	assert.Equal(t, "ListenBrainz", enricher.Name())
	assert.True(t, enricher.IsAvailable())
}

func TestNew_RespectsConfiguredAPIURL(t *testing.T) {
	cfg := &config.Config{}
	cfg.ListenBrainz.APIURL = "https://lb.example.com"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	enricher, err := New(logger, cfg)(context.Background(), nil)
	require.NoError(t, err)

	lb, ok := enricher.(*Enricher)
	require.True(t, ok)
	assert.Equal(t, "https://lb.example.com", lb.baseURL)
}

func TestNew_ImplementsExpectedInterfaces(t *testing.T) {
	cfg := &config.Config{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	enricher, err := New(logger, cfg)(context.Background(), nil)
	require.NoError(t, err)

	_, isArtist := enricher.(enrichers.ArtistEnricher)
	_, isTrack := enricher.(enrichers.TrackEnricher)
	_, isMatcher := enricher.(enrichers.IDMatcher)
	_, isAlbum := enricher.(enrichers.AlbumEnricher)

	assert.True(t, isArtist)
	assert.True(t, isTrack)
	assert.True(t, isMatcher)
	// ListenBrainz has no album-level metadata endpoint we consume.
	assert.False(t, isAlbum)
}

func TestRegistryRegistration(t *testing.T) {
	cfg := &config.Config{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	r := enrichers.NewRegistry()
	require.NoError(t, r.Register(enrichers.TypeListenBrainz, New(logger, cfg)))

	factory, ok := r.Get(enrichers.TypeListenBrainz)
	require.True(t, ok)

	enricher, err := factory(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, enricher)
	assert.Equal(t, enrichers.TypeListenBrainz, enricher.Type())
}

// --- MatchTrack (GET /1/metadata/lookup) ---
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-061

func TestMatchTrack_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/1/metadata/lookup", r.URL.Path)
		assert.Equal(t, "Rick Astley", r.URL.Query().Get("artist_name"))
		assert.Equal(t, "Never Gonna Give You Up", r.URL.Query().Get("recording_name"))
		assert.Equal(t, "Whenever You Need Somebody", r.URL.Query().Get("release_name"))
		// Governing: AGENTS.md "External API Etiquette" (descriptive User-Agent)
		assert.Equal(t, httputil.UserAgent, r.Header.Get("User-Agent"))

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(lbLookupResponse{
			ArtistCreditName: "Rick Astley",
			ArtistMBIDs:      []string{testArtistMBID},
			RecordingMBID:    testRecordingMBID,
			RecordingName:    "Never Gonna Give You Up",
		}))
	}))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	mbid, confidence, err := e.MatchTrack(context.Background(), "Never Gonna Give You Up", "Rick Astley", "Whenever You Need Somebody")
	require.NoError(t, err)
	assert.Equal(t, testRecordingMBID, mbid)
	assert.InDelta(t, lookupConfidence, confidence, 0.001)
}

func TestMatchTrack_OmitsEmptyReleaseName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasRelease := r.URL.Query()["release_name"]
		assert.False(t, hasRelease)
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(lbLookupResponse{RecordingMBID: testRecordingMBID}))
	}))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	mbid, _, err := e.MatchTrack(context.Background(), "Never Gonna Give You Up", "Rick Astley", "")
	require.NoError(t, err)
	assert.Equal(t, testRecordingMBID, mbid)
}

func TestMatchTrack_NoMatchEmptyObject(t *testing.T) {
	// The MBID mapper returns 200 with an empty JSON object on no match.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	mbid, confidence, err := e.MatchTrack(context.Background(), "Unknown Track", "Unknown Artist", "")
	require.NoError(t, err)
	assert.Empty(t, mbid)
	assert.Zero(t, confidence)
}

func TestMatchTrack_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":404,"error":"not found"}`, http.StatusNotFound)
	}))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	mbid, confidence, err := e.MatchTrack(context.Background(), "Track", "Artist", "")
	require.NoError(t, err)
	assert.Empty(t, mbid)
	assert.Zero(t, confidence)
}

func TestMatchTrack_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"recording_mbid": `))
	}))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	_, _, err := e.MatchTrack(context.Background(), "Track", "Artist", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}

// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-063 (429 honored before retry)
func TestMatchTrack_RateLimitedThenSuccess(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(lbLookupResponse{RecordingMBID: testRecordingMBID}))
	}))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	mbid, _, err := e.MatchTrack(context.Background(), "Track", "Artist", "")
	require.NoError(t, err)
	assert.Equal(t, testRecordingMBID, mbid)
	assert.Equal(t, 2, attempts)
}

// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-063 (abort when
// advertised wait exceeds the cap rather than retrying early)
func TestMatchTrack_RateLimitExceedsCapAborts(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	_, _, err := e.MatchTrack(context.Background(), "Track", "Artist", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")
	assert.Equal(t, 1, attempts, "must not retry early when advertised wait exceeds the cap")
}

func TestMatchTrack_MissingNamesSkipsRequest(t *testing.T) {
	server := failServer(t)
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	mbid, confidence, err := e.MatchTrack(context.Background(), "", "Artist", "")
	require.NoError(t, err)
	assert.Empty(t, mbid)
	assert.Zero(t, confidence)

	mbid, confidence, err = e.MatchTrack(context.Background(), "Track", "", "")
	require.NoError(t, err)
	assert.Empty(t, mbid)
	assert.Zero(t, confidence)
}

// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-061 (artist/album
// matching not supported — fall through to other IDMatchers, no HTTP calls)
func TestMatchArtistAndAlbum_NotSupported(t *testing.T) {
	server := failServer(t)
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	mbid, confidence, err := e.MatchArtist(context.Background(), "Portishead")
	require.NoError(t, err)
	assert.Empty(t, mbid)
	assert.Zero(t, confidence)

	mbid, confidence, err = e.MatchAlbum(context.Background(), "Dummy", "Portishead")
	require.NoError(t, err)
	assert.Empty(t, mbid)
	assert.Zero(t, confidence)
}

// --- EnrichArtist (GET /1/metadata/artist/ + POST /1/popularity/artist) ---
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-062, REQ-ENRICH-064

func artistHandler(t *testing.T, listenCount *int64) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/1/metadata/artist/":
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, testArtistMBID, r.URL.Query().Get("artist_mbids"))
			assert.Equal(t, "tag", r.URL.Query().Get("inc"))
			require.NoError(t, json.NewEncoder(w).Encode([]lbArtistMetadata{
				{
					ArtistMBID: testArtistMBID,
					Name:       "Portishead",
					Type:       "Group",
					Area:       "United Kingdom",
					Tag: lbTagBlock{
						Artist: []lbTag{
							{Count: 3, Tag: "electronic"},
							{Count: 10, Tag: "trip hop"},
							{Count: 5, Tag: "downtempo"},
							{Count: 1, Tag: ""},
						},
					},
				},
			}))
		case "/1/popularity/artist":
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			var body struct {
				ArtistMBIDs []string `json:"artist_mbids"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, []string{testArtistMBID}, body.ArtistMBIDs)
			require.NoError(t, json.NewEncoder(w).Encode([]lbArtistPopularity{
				{ArtistMBID: testArtistMBID, TotalListenCount: listenCount, TotalUserCount: int64Ptr(42)},
			}))
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
	}
}

func TestEnrichArtist_Success(t *testing.T) {
	server := httptest.NewServer(artistHandler(t, int64Ptr(1_000_000)))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	artist := &ent.Artist{ID: 1, Name: "Portishead", MusicbrainzID: testArtistMBID}

	data, err := e.EnrichArtist(context.Background(), artist)
	require.NoError(t, err)
	require.NotNil(t, data)

	// Tags sorted by vote count descending, empty names dropped.
	assert.Equal(t, []string{"trip hop", "downtempo", "electronic"}, data.Tags)
	require.Len(t, data.TypedTags, 3)
	assert.Equal(t, "trip hop", data.TypedTags[0].Name)
	assert.Equal(t, "genre", data.TypedTags[0].Type)

	// 10^6 listens log-scales to 86 on the 0-100 popularity scale.
	require.NotNil(t, data.Popularity)
	assert.Equal(t, 86, *data.Popularity)
}

func TestEnrichArtist_NoMBIDSkips(t *testing.T) {
	server := failServer(t)
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	data, err := e.EnrichArtist(context.Background(), &ent.Artist{Name: "Portishead"})
	require.NoError(t, err)
	assert.Nil(t, data)
}

func TestEnrichArtist_UnknownMBIDReturnsNil(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Unknown MBIDs are omitted from the response array.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	data, err := e.EnrichArtist(context.Background(), &ent.Artist{Name: "Nobody", MusicbrainzID: testArtistMBID})
	require.NoError(t, err)
	assert.Nil(t, data)
}

func TestEnrichArtist_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	data, err := e.EnrichArtist(context.Background(), &ent.Artist{Name: "Nobody", MusicbrainzID: testArtistMBID})
	require.NoError(t, err)
	assert.Nil(t, data)
}

func TestEnrichArtist_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"not": "an array"`))
	}))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	_, err := e.EnrichArtist(context.Background(), &ent.Artist{Name: "X", MusicbrainzID: testArtistMBID})
	require.Error(t, err)
}

// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-062 (popularity
// failures must not discard other enrichment data)
func TestEnrichArtist_PopularityFailureIsBestEffort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/1/popularity/artist" {
			http.Error(w, "boom", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode([]lbArtistMetadata{
			{ArtistMBID: testArtistMBID, Name: "Portishead", Tag: lbTagBlock{Artist: []lbTag{{Count: 1, Tag: "trip hop"}}}},
		}))
	}))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	data, err := e.EnrichArtist(context.Background(), &ent.Artist{Name: "Portishead", MusicbrainzID: testArtistMBID})
	require.NoError(t, err)
	require.NotNil(t, data)
	assert.Equal(t, []string{"trip hop"}, data.Tags)
	assert.Nil(t, data.Popularity)
}

func TestEnrichArtist_NullPopularityCount(t *testing.T) {
	server := httptest.NewServer(artistHandler(t, nil))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	data, err := e.EnrichArtist(context.Background(), &ent.Artist{Name: "Portishead", MusicbrainzID: testArtistMBID})
	require.NoError(t, err)
	require.NotNil(t, data)
	assert.Nil(t, data.Popularity, "null total_listen_count must not become a popularity score")
}

func TestGetArtistImages_ReturnsNil(t *testing.T) {
	server := failServer(t)
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	images, err := e.GetArtistImages(context.Background(), &ent.Artist{Name: "X", MusicbrainzID: testArtistMBID})
	require.NoError(t, err)
	assert.Nil(t, images)
}

// --- EnrichTrack (GET /1/metadata/recording/ + POST /1/popularity/recording) ---
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-061, REQ-ENRICH-062

func recordingHandler(t *testing.T, withLookup bool) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/1/metadata/lookup":
			if !withLookup {
				t.Errorf("unexpected lookup request")
				return
			}
			require.NoError(t, json.NewEncoder(w).Encode(lbLookupResponse{RecordingMBID: testRecordingMBID}))
		case "/1/metadata/recording/":
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, testRecordingMBID, r.URL.Query().Get("recording_mbids"))
			assert.Equal(t, "artist tag release", r.URL.Query().Get("inc"))
			meta := lbRecordingMetadata{}
			meta.Recording.Name = "Sour Times"
			meta.Recording.Length = 253000
			meta.Release.Name = "Dummy"
			meta.Release.Year = 1994
			meta.Tag = lbTagBlock{
				Recording: []lbTag{
					{Count: 2, Tag: "electronic"},
					{Count: 4, Tag: "trip hop"},
				},
				Artist: []lbTag{{Count: 9, Tag: "artist-only-tag"}},
			}
			require.NoError(t, json.NewEncoder(w).Encode(map[string]lbRecordingMetadata{
				testRecordingMBID: meta,
			}))
		case "/1/popularity/recording":
			assert.Equal(t, http.MethodPost, r.Method)
			var body struct {
				RecordingMBIDs []string `json:"recording_mbids"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, []string{testRecordingMBID}, body.RecordingMBIDs)
			require.NoError(t, json.NewEncoder(w).Encode([]lbRecordingPopularity{
				{RecordingMBID: testRecordingMBID, TotalListenCount: int64Ptr(10_000_000), TotalUserCount: int64Ptr(7)},
			}))
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
	}
}

func TestEnrichTrack_WithExistingMBID(t *testing.T) {
	server := httptest.NewServer(recordingHandler(t, false))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	track := &ent.Track{Name: "Sour Times", MusicbrainzID: strPtr(testRecordingMBID)}

	data, err := e.EnrichTrack(context.Background(), track)
	require.NoError(t, err)
	require.NotNil(t, data)

	// Recording tags only (artist tags belong to artist enrichment),
	// sorted by vote count descending.
	assert.Equal(t, []string{"trip hop", "electronic"}, data.Tags)
	assert.NotContains(t, data.Tags, "artist-only-tag")
	assert.Equal(t, 253000, data.DurationMs)
	// MBID already known: nothing new to persist.
	assert.Empty(t, data.MusicBrainzID)
	// 10^7 listens is the reference max on the 0-100 scale.
	require.NotNil(t, data.Popularity)
	assert.Equal(t, 100, *data.Popularity)
}

func TestEnrichTrack_ResolvesMBIDViaLookup(t *testing.T) {
	server := httptest.NewServer(recordingHandler(t, true))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	track := &ent.Track{
		Name: "Sour Times",
		Edges: ent.TrackEdges{
			Artist: &ent.Artist{Name: "Portishead"},
			Album:  &ent.Album{Name: "Dummy"},
		},
	}

	data, err := e.EnrichTrack(context.Background(), track)
	require.NoError(t, err)
	require.NotNil(t, data)

	// Freshly matched MBID is surfaced so the pipeline can persist it.
	assert.Equal(t, testRecordingMBID, data.MusicBrainzID)
	assert.Equal(t, []string{"trip hop", "electronic"}, data.Tags)
}

func TestEnrichTrack_NoMBIDNoArtistSkips(t *testing.T) {
	server := failServer(t)
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	data, err := e.EnrichTrack(context.Background(), &ent.Track{Name: "Orphan"})
	require.NoError(t, err)
	assert.Nil(t, data)
}

func TestEnrichTrack_LookupNoMatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/1/metadata/lookup", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	track := &ent.Track{
		Name:  "Unknown",
		Edges: ent.TrackEdges{Artist: &ent.Artist{Name: "Unknown Artist"}},
	}

	data, err := e.EnrichTrack(context.Background(), track)
	require.NoError(t, err)
	assert.Nil(t, data)
}

func TestEnrichTrack_UnknownMBIDReturnsNil(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Unknown MBIDs are omitted from the keyed response object.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	track := &ent.Track{Name: "Ghost", MusicbrainzID: strPtr(testRecordingMBID)}

	data, err := e.EnrichTrack(context.Background(), track)
	require.NoError(t, err)
	assert.Nil(t, data)
}

func TestEnrichTrack_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	track := &ent.Track{Name: "Ghost", MusicbrainzID: strPtr(testRecordingMBID)}

	data, err := e.EnrichTrack(context.Background(), track)
	require.NoError(t, err)
	assert.Nil(t, data)
}

func TestEnrichTrack_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[not json`))
	}))
	defer server.Close()

	e := createTestEnricher(t, server.URL)

	track := &ent.Track{Name: "Ghost", MusicbrainzID: strPtr(testRecordingMBID)}

	_, err := e.EnrichTrack(context.Background(), track)
	require.Error(t, err)
}

// --- Popularity mapping ---
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-062

func TestPopularityScore(t *testing.T) {
	tests := []struct {
		name  string
		count int64
		want  int
	}{
		{"negative", -5, 0},
		{"zero", 0, 0},
		{"single listen floors at 1", 1, 4},
		{"thousand listens", 1_000, 43},
		{"million listens", 1_000_000, 86},
		{"reference max", 10_000_000, 100},
		{"beyond reference caps at 100", 5_000_000_000, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, popularityScore(tt.count))
		})
	}
}

// --- Rate limit parsing ---
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-063

func TestRateLimitWait(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		wantWait time.Duration
		wantOK   bool
	}{
		{"no headers defaults to 1s", nil, time.Second, true},
		{"retry-after seconds", map[string]string{"Retry-After": "5"}, 5 * time.Second, true},
		{"retry-after zero", map[string]string{"Retry-After": "0"}, 0, true},
		{"x-ratelimit-reset-in fallback", map[string]string{"X-RateLimit-Reset-In": "3"}, 3 * time.Second, true},
		{"retry-after preferred over reset-in", map[string]string{"Retry-After": "2", "X-RateLimit-Reset-In": "9"}, 2 * time.Second, true},
		{"unparseable defaults to 1s", map[string]string{"Retry-After": "soon"}, time.Second, true},
		{"exceeds cap aborts", map[string]string{"Retry-After": "3600"}, 0, false},
		{"reset-in exceeds cap aborts", map[string]string{"X-RateLimit-Reset-In": "120"}, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tt.headers {
				h.Set(k, v)
			}
			wait, ok := rateLimitWait(h)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantWait, wait)
		})
	}
}

func TestRateLimitWait_HTTPDate(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", time.Now().Add(4*time.Second).UTC().Format(http.TimeFormat))

	wait, ok := rateLimitWait(h)
	assert.True(t, ok)
	assert.Greater(t, wait, time.Duration(0))
	assert.LessOrEqual(t, wait, 5*time.Second)
}
