package schema

import (
	"fmt"
	"regexp"
)

// Governing: AGENTS.md VAL-007 (MusicBrainz IDs MUST be in correct UUID format)
//
// MusicBrainz MBIDs are UUIDs. MusicBrainz canonically emits lowercase hex,
// but MBIDs can also arrive from file tags via Navidrome, where uppercase hex
// is possible; both cases are accepted. Values are deliberately NOT
// lowercase-normalized on write: stored MBIDs are compared byte-for-byte
// against external API responses (e.g. Lidarr's ForeignArtistID/ForeignAlbumID
// in internal/enrichers/lidarr and internal/services/lidarr_submitter), and
// rewriting the case could desynchronize those comparisons.
var musicBrainzIDRegexp = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsValidMusicBrainzID reports whether s is a well-formed UUID (upper or
// lower case hex). It is exported so write paths (e.g. the metadata
// enrichment service) can pre-check enricher-supplied values and skip invalid
// ones instead of failing an entire update.
// Governing: AGENTS.md VAL-007
func IsValidMusicBrainzID(s string) bool {
	return musicBrainzIDRegexp.MatchString(s)
}

// validateOptionalMusicBrainzID validates a musicbrainz_id field value.
//
// Empty string is explicitly allowed: the fields are Optional and unenriched
// entities legitimately carry an empty value. Ent runs field validators on
// any value present in a mutation — including an explicit
// SetMusicbrainzID("") — so the empty case must be handled here rather than
// relying on Optional alone (a bare field.Match() would reject "").
// Governing: AGENTS.md VAL-007
func validateOptionalMusicBrainzID(s string) error {
	if s == "" {
		return nil
	}
	if !IsValidMusicBrainzID(s) {
		return fmt.Errorf("musicbrainz_id %q is not a valid UUID", s)
	}
	return nil
}
