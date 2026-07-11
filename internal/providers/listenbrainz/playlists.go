// Governing: SPEC music-provider-integration REQ "ListenBrainz Provider" (REQ-PROV-049),
// ADR-0014 (three-tier track matching — recording MBIDs ride the provider track ID slot)
package listenbrainz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"spotter/ent/schema"
	"spotter/internal/providers"
)

// ErrPlaylistWriteNotSupported is returned by playlist write methods.
// ListenBrainz playlist sync is deliberately read-only: playlists (including
// generated ones like Weekly Jams) are imported INTO Spotter, never written
// back. The PlaylistManager interface bundles CreatePlaylist with
// GetPlaylists, so the write method exists but always fails with this
// sentinel.
// Governing: SPEC music-provider-integration REQ-PROV-049 (read-only playlist sync)
var ErrPlaylistWriteNotSupported = errors.New("listenbrainz: playlist writes are not supported (playlist sync is read-only)")

// Governing: SPEC music-provider-integration REQ-PROV-049 (ListenBrainz implements
// the read side of PlaylistManager)
var _ providers.PlaylistManager = (*Provider)(nil)

const (
	// playlistsPageSize is the per-request playlist count for the user
	// playlist listing endpoints, which paginate via count/offset. The API
	// default is 25; 50 stays well within the documented maximum of 100.
	// Governing: SPEC music-provider-integration REQ-PROV-049
	playlistsPageSize = 50

	// recordingURIPath is the host+path fragment of the identifier URI JSPF
	// tracks use to carry a MusicBrainz recording MBID
	// (http(s)://musicbrainz.org/recording/<mbid>).
	recordingURIPath = "musicbrainz.org/recording/"
)

// jspfIdentifier accepts both encodings of the JSPF "identifier" field. The
// JSPF spec (xspf.org/jspf) defines identifier as a single URI string, but
// ListenBrainz emits track identifiers as a LIST of URI strings; playlist
// stubs use the plain string form. Any other JSON shape is a malformed
// response.
// Governing: SPEC music-provider-integration REQ-PROV-049 (identifier: string or list of strings)
type jspfIdentifier []string

func (l *jspfIdentifier) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if s == "" {
			*l = nil
			return nil
		}
		*l = jspfIdentifier{s}
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		*l = jspfIdentifier(list)
		return nil
	}
	return fmt.Errorf("jspf identifier must be a URI string or a list of URI strings")
}

// jspfTrack mirrors a JSPF track object. Durations are in milliseconds.
type jspfTrack struct {
	Title      string         `json:"title"`
	Creator    string         `json:"creator"` // artist name
	Album      string         `json:"album"`
	DurationMs int            `json:"duration"`
	Identifier jspfIdentifier `json:"identifier"`
}

// jspfPlaylist mirrors a JSPF playlist object. List endpoints return stubs
// (no track array); GET /1/playlist/{playlist_mbid} returns the full form.
type jspfPlaylist struct {
	Title      string         `json:"title"`
	Annotation string         `json:"annotation"` // description, may contain HTML
	Creator    string         `json:"creator"`
	Identifier jspfIdentifier `json:"identifier"` // https://listenbrainz.org/playlist/<mbid>
	Tracks     []jspfTrack    `json:"track"`
}

// playlistListResponse mirrors GET /1/user/{username}/playlists and
// GET /1/user/{username}/playlists/createdfor.
type playlistListResponse struct {
	PlaylistCount int `json:"playlist_count"`
	Count         int `json:"count"`
	Offset        int `json:"offset"`
	Playlists     []struct {
		Playlist jspfPlaylist `json:"playlist"`
	} `json:"playlists"`
}

// playlistResponse mirrors GET /1/playlist/{playlist_mbid}.
type playlistResponse struct {
	Playlist jspfPlaylist `json:"playlist"`
}

// Governing: SPEC music-provider-integration REQ-PROV-003 (GetPlaylists), REQ-PROV-049
// GetPlaylists retrieves the user's ListenBrainz playlists as JSPF and
// normalizes them. Two listing endpoints are combined:
//
//   - /1/user/{username}/playlists — playlists the user created themselves.
//   - /1/user/{username}/playlists/createdfor — playlists GENERATED FOR the
//     user by ListenBrainz (troi-bot), e.g. Weekly Jams, Weekly Exploration,
//     Daily Jams. These are the most valuable to import and would be missed
//     by the created-by endpoint alone, so both are always fetched.
//
// The list endpoints return JSPF stubs without tracks; full contents are
// fetched per playlist via GET /1/playlist/{playlist_mbid}.
func (p *Provider) GetPlaylists(ctx context.Context) ([]providers.Playlist, error) {
	p.logger.Info("fetching playlists from listenbrainz", "username", p.auth.Username)

	userPath := "/1/user/" + url.PathEscape(p.auth.Username) + "/playlists"

	// User-created playlists.
	stubs, err := p.fetchPlaylistStubs(ctx, userPath)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch listenbrainz playlists: %w", err)
	}
	// Playlists created FOR the user (Weekly Jams etc.) — see doc comment.
	createdFor, err := p.fetchPlaylistStubs(ctx, userPath+"/createdfor")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch listenbrainz createdfor playlists: %w", err)
	}
	stubs = append(stubs, createdFor...)

	playlists := make([]providers.Playlist, 0, len(stubs))
	seen := make(map[string]bool)
	for _, stub := range stubs {
		mbid := playlistMBID(stub.Identifier)
		if mbid == "" {
			p.logger.Warn("skipping listenbrainz playlist without a parseable playlist MBID",
				"title", stub.Title, "identifier", strings.Join(stub.Identifier, ","))
			continue
		}
		// A playlist can plausibly appear in both listings; the remote ID
		// (playlist MBID) is the upsert key downstream, so dedupe here.
		if seen[mbid] {
			continue
		}
		seen[mbid] = true

		pl, err := p.getFullPlaylist(ctx, mbid)
		if err != nil {
			// Keep the stub (without tracks) instead of failing the whole
			// sync or dropping the playlist: dropping it would cause the
			// reconciler to deactivate it locally, and persistPlaylistTracks
			// leaves existing tracks untouched when none are provided.
			// Governing: AGENTS.md ERR-004 (external API failures degrade gracefully)
			p.logger.Warn("failed to fetch full listenbrainz playlist, keeping stub without tracks",
				"playlist_mbid", mbid, "title", stub.Title, "error", err)
			playlists = append(playlists, p.normalizePlaylist(mbid, stub))
			continue
		}
		playlists = append(playlists, p.normalizePlaylist(mbid, pl))
	}

	p.logger.Info("fetched playlists from listenbrainz", "count", len(playlists))
	return playlists, nil
}

// Governing: SPEC music-provider-integration REQ-PROV-003 (CreatePlaylist),
// REQ-PROV-049 (read-only: playlist writes MUST fail with ErrPlaylistWriteNotSupported)
// CreatePlaylist always fails: ListenBrainz playlist sync is read-only.
func (p *Provider) CreatePlaylist(ctx context.Context, name, description string, tracks []providers.Track) error {
	return ErrPlaylistWriteNotSupported
}

// fetchPlaylistStubs pages through a playlist listing endpoint using
// count/offset pagination and returns all JSPF stubs.
// Governing: SPEC music-provider-integration REQ-PROV-049 (count/offset pagination)
func (p *Provider) fetchPlaylistStubs(ctx context.Context, path string) ([]jspfPlaylist, error) {
	var stubs []jspfPlaylist
	offset := 0
	for {
		query := url.Values{}
		query.Set("count", strconv.Itoa(playlistsPageSize))
		query.Set("offset", strconv.Itoa(offset))

		var result playlistListResponse
		if err := p.doRequest(ctx, "GET", path+"?"+query.Encode(), p.auth.Token, &result); err != nil {
			return nil, err
		}

		for _, item := range result.Playlists {
			stubs = append(stubs, item.Playlist)
		}
		p.logger.Debug("fetched playlist stubs page",
			"path", path, "offset", offset, "page", len(result.Playlists), "total", result.PlaylistCount)

		// A short/empty page or reaching the advertised total ends
		// pagination. len(result.Playlists) == 0 also guards against a
		// misbehaving server that keeps advertising a larger playlist_count
		// than it serves (the offset would otherwise never catch up).
		offset += len(result.Playlists)
		if len(result.Playlists) < playlistsPageSize || offset >= result.PlaylistCount {
			return stubs, nil
		}
	}
}

// getFullPlaylist fetches the complete JSPF (including tracks) for one playlist.
// Governing: SPEC music-provider-integration REQ-PROV-049 (GET /1/playlist/{playlist_mbid})
func (p *Provider) getFullPlaylist(ctx context.Context, mbid string) (jspfPlaylist, error) {
	var result playlistResponse
	if err := p.doRequest(ctx, "GET", "/1/playlist/"+url.PathEscape(mbid), p.auth.Token, &result); err != nil {
		return jspfPlaylist{}, err
	}
	return result.Playlist, nil
}

// normalizePlaylist converts a JSPF playlist into the provider-neutral shape
// consumed by the sync layer (persistPlaylists / persistPlaylistTracks).
func (p *Provider) normalizePlaylist(mbid string, pl jspfPlaylist) providers.Playlist {
	artists := make(map[string]struct{})
	albums := make(map[string]struct{})
	tracks := make([]providers.Track, 0, len(pl.Tracks))

	for _, t := range pl.Tracks {
		if t.Title == "" || t.Creator == "" {
			// The persist layer requires track and artist names; skip early.
			p.logger.Debug("skipping jspf track without title or creator", "playlist_mbid", mbid, "title", t.Title)
			continue
		}
		artists[t.Creator] = struct{}{}
		if t.Album != "" {
			albums[t.Album] = struct{}{}
		}

		// JSPF tracks identify by MusicBrainz recording MBID. The MBID rides
		// in Track.ID (the provider-track-ID slot, persisted as
		// PlaylistTrack.remote_id); the matching pipeline has no MBID tier,
		// so catalog linking falls back to name/artist matching (ADR-0014
		// tiers 2/3 — ListenBrainz supplies no ISRC for tier 1).
		// Governing: ADR-0014, SPEC music-provider-integration REQ-PROV-049
		mbidURI := ""
		recMBID := ""
		for _, id := range t.Identifier {
			if m := recordingMBIDFromURI(id); m != "" {
				recMBID = m
				mbidURI = id
				break
			}
		}

		tracks = append(tracks, providers.Track{
			ID:         recMBID,
			Name:       t.Title,
			Artist:     t.Creator,
			Album:      t.Album,
			DurationMs: t.DurationMs,
			URL:        mbidURI,
		})
	}

	externalURL := ""
	if len(pl.Identifier) > 0 {
		externalURL = pl.Identifier[0]
	}

	return providers.Playlist{
		ID:            mbid,
		Name:          pl.Title,
		Description:   strings.TrimSpace(stripHTML(pl.Annotation)),
		ExternalURL:   externalURL,
		TrackCount:    len(tracks),
		UniqueArtists: len(artists),
		UniqueAlbums:  len(albums),
		Tracks:        tracks,
	}
}

// recordingMBIDFromURI extracts the MusicBrainz recording MBID from a JSPF
// identifier URI of the form http(s)://musicbrainz.org/recording/<mbid>.
// It returns "" when the URI is not a recording URI or the MBID is not a
// well-formed UUID.
// Governing: SPEC music-provider-integration REQ-PROV-049 (MBID identity)
func recordingMBIDFromURI(uri string) string {
	idx := strings.Index(uri, recordingURIPath)
	if idx < 0 {
		return ""
	}
	// Only accept the expected scheme+host forms, not arbitrary URLs that
	// merely embed the substring somewhere in their path.
	prefix := uri[:idx]
	if prefix != "https://" && prefix != "http://" {
		return ""
	}
	mbid := strings.TrimSuffix(uri[idx+len(recordingURIPath):], "/")
	if !schema.IsValidMusicBrainzID(mbid) {
		return ""
	}
	return mbid
}

// playlistMBID extracts the playlist MBID from a JSPF playlist identifier of
// the form https://listenbrainz.org/playlist/<mbid>. The MBID (not the full
// URL) is the stable remote ID used for upserts.
func playlistMBID(ids jspfIdentifier) string {
	for _, uri := range ids {
		trimmed := strings.TrimSuffix(uri, "/")
		if idx := strings.LastIndex(trimmed, "/playlist/"); idx >= 0 {
			mbid := trimmed[idx+len("/playlist/"):]
			if schema.IsValidMusicBrainzID(mbid) {
				return mbid
			}
		}
	}
	return ""
}

// stripHTML removes HTML tags from a string. ListenBrainz playlist
// annotations (especially generated playlists like Weekly Jams) contain HTML
// markup; descriptions are stored as plain text.
func stripHTML(s string) string {
	var result strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			result.WriteRune(r)
		}
	}
	return result.String()
}
