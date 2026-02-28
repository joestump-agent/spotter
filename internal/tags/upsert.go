// Governing: SPEC-0014 REQ "Enricher Integration", SPEC-0014 REQ "Denormalized Entity Tags Table"
package tags

import (
	"context"
)

// TypedTag represents a tag with a specific type classification.
// Governing: SPEC-0014 REQ "Enricher Integration"
type TypedTag struct {
	Name string
	Type string // "id3", "genre", "ai", "label", "source"
}

// UpsertTagsForEntity creates or retrieves Tag entities for the given typed tags
// and associates them with the specified entity. Also maintains the entity_tags
// denormalized table.
// NOTE: This is a stub until ent Tag schema (PR #255) is merged.
// Governing: SPEC-0014 REQ "Enricher Integration", SPEC-0014 REQ "Denormalized Entity Tags Table"
func UpsertTagsForEntity(ctx context.Context, userID int, entityType string, entityID int, tags []TypedTag) error {
	// Full implementation pending ent Tag schema merge (PR #255).
	// Enrichers call this; it will write to the tags table and entity_tags table.
	return nil
}
