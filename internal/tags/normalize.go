// Governing: SPEC-0014 REQ "Tag Normalization"
package tags

import (
	"strings"
	"unicode"
)

// Normalize returns the canonical lowercase, whitespace-trimmed,
// whitespace-collapsed form of a tag name.
func Normalize(name string) string {
	return strings.ToLower(DisplayName(name))
}

// DisplayName returns the display form of a tag name: whitespace-trimmed and
// whitespace-collapsed, but with the original casing preserved. It mirrors
// Normalize so that Normalize(DisplayName(s)) == Normalize(s); e.g.
// "  Shoegaze  " becomes display name "Shoegaze" with normalized key
// "shoegaze".
func DisplayName(name string) string {
	fields := strings.FieldsFunc(strings.TrimSpace(name), unicode.IsSpace)
	return strings.Join(fields, " ")
}
