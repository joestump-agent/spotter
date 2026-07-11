package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Listen holds the schema definition for the Listen entity.
type Listen struct {
	ent.Schema
}

// Fields of the Listen.
func (Listen) Fields() []ent.Field {
	return []ent.Field{
		field.String("track_name"),
		field.String("artist_name"),
		field.String("album_name"),
		field.String("source"), // e.g., "spotify", "navidrome"
		field.Time("played_at"),
		field.String("url").
			Optional(),
		// Governing: SPEC listen-playlist-sync REQ-SYNC-021 (dedup key includes provider track ID)
		field.String("provider_track_id").
			Optional().
			Default("").
			MaxLen(2048).
			Comment("Provider-specific track ID used for de-duplication when available"),
		// Governing: SPEC music-provider-integration REQ "ListenBrainz Listen Submission" (REQ-PROV-049)
		// Nullable on purpose: Ent auto-migration adds a NULL-able column safely
		// on databases that already contain rows (a NOT NULL column with only a
		// Go-side default would fail to migrate — see the PR #39 regression in
		// internal/database/backfill_timestamps_test.go). NULL means "not yet
		// processed"; repeat syncs only select rows where this is NULL, making
		// the submission pipeline idempotent.
		//
		// The stamp means "processed for ListenBrainz submission", NOT strictly
		// "accepted": it is set when the listen was (a) submitted and accepted,
		// (b) permanently rejected by ListenBrainz as unsubmittable (stamping
		// keeps a poison listen from wedging the oldest-first submission queue
		// forever), or (c) skipped because ListenBrainz already has the play
		// natively (a listenbrainz-source sibling row exists within the
		// cross-provider dedup window).
		field.Time("submitted_to_listenbrainz_at").
			Optional().
			Nillable().
			Comment("When this listen was processed for ListenBrainz submission: " +
				"submitted and accepted, permanently rejected as unsubmittable, " +
				"or skipped as already present in ListenBrainz (NULL = not yet processed)"),
	}
}

// Edges of the Listen.
func (Listen) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("listens").
			Unique().
			Required(),
		// Optional edges to matched library entities
		edge.From("artist", Artist.Type).
			Ref("listens").
			Unique(),
		edge.From("album", Album.Type).
			Ref("listens").
			Unique(),
		edge.From("track", Track.Type).
			Ref("listens").
			Unique(),
	}
}

// Indexes of the Listen.
func (Listen) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("played_at", "source", "track_name", "artist_name").
			Edges("user").
			Unique(),
	}
}
