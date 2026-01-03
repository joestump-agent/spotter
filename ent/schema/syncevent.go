package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SyncEvent holds the schema definition for the SyncEvent entity.
type SyncEvent struct {
	ent.Schema
}

// Fields of the SyncEvent.
func (SyncEvent) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("event_type").
			Values(
				// Sync events
				"sync_started",
				"track_added",
				"track_skipped",
				"playlist_added",
				"playlist_skipped",
				"sync_completed",
				"sync_failed",
				// Metadata enrichment events
				"metadata_started",
				"metadata_completed",
				"metadata_failed",
				"artist_enriched",
				"album_enriched",
				"track_enriched",
				"image_downloaded",
				"catalog_built",
				// Cleanup/maintenance events
				"cleanup_started",
				"cleanup_completed",
				"data_reset",
			).
			Comment("The type of sync event"),
		field.String("provider").
			Comment("The provider that triggered this event (spotify, navidrome, lastfm, metadata, system)"),
		field.String("message").
			Comment("Human-readable message describing the event"),
		field.String("metadata").
			Optional().
			Comment("Optional JSON metadata with additional details"),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			Comment("When the event occurred"),
	}
}

// Edges of the SyncEvent.
func (SyncEvent) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("sync_events").
			Unique().
			Required(),
	}
}

// Indexes of the SyncEvent.
func (SyncEvent) Indexes() []ent.Index {
	return []ent.Index{
		// Index for querying events by user and time
		index.Fields("created_at").
			Edges("user"),
		// Index for filtering by provider
		index.Fields("provider").
			Edges("user"),
		// Index for filtering by event type
		index.Fields("event_type").
			Edges("user"),
	}
}
