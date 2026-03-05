package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	spmixin "spotter/ent/schema/mixin"
)

// LidarrQueue holds the schema definition for the LidarrQueue entity.
// Governing: SPEC-0017 REQ "Enricher Decoupling", ADR-0029
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
			Positive().
			Comment("ID of the artist or album entity"),
		field.String("musicbrainz_id").
			NotEmpty().
			MaxLen(36).
			Comment("MusicBrainz ID for the entity"),
		field.Enum("status").
			Values("queued", "submitted", "failed").
			Default("queued").
			Comment("Current status of the queue entry"),
		field.Int("attempts").
			Default(0).
			Comment("Number of submission attempts"),
		field.String("last_error").
			Optional().
			Nillable().
			Comment("Error message from the last failed attempt"),
		field.Time("retry_at").
			Optional().
			Nillable().
			Comment("When to retry the submission"),
	}
}

// Edges of the LidarrQueue.
func (LidarrQueue) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("lidarr_queue").
			Unique().
			Required(),
	}
}

// Indexes of the LidarrQueue.
func (LidarrQueue) Indexes() []ent.Index {
	return []ent.Index{
		// Unique constraint: one queue entry per entity_type+entity_id+user
		index.Fields("entity_type", "entity_id").
			Edges("user").
			Unique(),
		// Index for polling queued items
		index.Fields("status"),
		// Index for retry scheduling
		index.Fields("retry_at"),
	}
}
