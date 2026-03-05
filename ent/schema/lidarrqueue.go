// Governing: SPEC-0017 REQ "Background Submitter Goroutine", ADR-0029, ADR-0013

package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	spmixin "spotter/ent/schema/mixin"
)

// LidarrQueue holds the schema definition for the LidarrQueue entity.
// It represents items queued for submission to Lidarr (artists or albums).
type LidarrQueue struct {
	ent.Schema
}

// Mixin of the LidarrQueue.
func (LidarrQueue) Mixin() []ent.Mixin {
	return []ent.Mixin{
		spmixin.Timestamps{},
	}
}

// Fields of the LidarrQueue.
func (LidarrQueue) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("entity_type").
			Values("artist", "album").
			Comment("Type of entity to submit to Lidarr"),
		field.Int("entity_id").
			Comment("ID of the artist or album entity"),
		field.String("musicbrainz_id").
			NotEmpty().
			Comment("MusicBrainz ID for the entity"),
		field.Enum("status").
			Values("queued", "submitted", "failed").
			Default("queued").
			Comment("Current status of the queue item"),
		field.Int("attempts").
			Default(0).
			Comment("Number of submission attempts"),
		field.String("last_error").
			Optional().
			Comment("Error message from last failed attempt"),
		field.Time("retry_at").
			Optional().
			Nillable().
			Comment("When to retry a failed submission"),
		field.Time("submitted_at").
			Optional().
			Nillable().
			Comment("When the item was successfully submitted"),
		field.Int("lidarr_id").
			Optional().
			Comment("Lidarr ID returned after successful submission"),
	}
}

// Edges of the LidarrQueue.
func (LidarrQueue) Edges() []ent.Edge {
	return nil
}

// Indexes of the LidarrQueue.
func (LidarrQueue) Indexes() []ent.Index {
	return []ent.Index{
		// Index for querying eligible items (status + retry_at)
		index.Fields("status", "retry_at"),
		// Index for finding items by entity
		index.Fields("entity_type", "entity_id").
			Unique(),
		// Index for cleanup of old submitted items
		index.Fields("status", "submitted_at"),
		// Index for ordering by created_at
		index.Fields("created_at"),
	}
}

// CleanupAge is the duration after which submitted items are deleted.
const CleanupAge = 7 * 24 * time.Hour
