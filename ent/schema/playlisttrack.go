package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// PlaylistTrack holds the schema definition for the PlaylistTrack entity.
// This represents a track within a playlist, storing both the raw provider data
// and optional links to enriched catalog entries.
type PlaylistTrack struct {
	ent.Schema
}

// Fields of the PlaylistTrack.
func (PlaylistTrack) Fields() []ent.Field {
	return []ent.Field{
		// Raw data from the provider
		field.String("track_name").
			NotEmpty(),
		field.String("artist_name").
			NotEmpty(),
		field.String("album_name").
			Optional(),
		field.String("remote_id").
			Optional().
			Comment("Provider-specific track ID"),
		field.Int("position").
			Default(0).
			Comment("Position/order in the playlist"),
		field.Int("duration_ms").
			Optional().
			Nillable().
			Comment("Track duration in milliseconds from provider"),
		field.String("isrc").
			Optional().
			Nillable().
			Comment("International Standard Recording Code for track matching"),
		field.String("url").
			Optional().
			Comment("Deep link to track on provider"),
		field.Time("created_at").
			Default(time.Now),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the PlaylistTrack.
func (PlaylistTrack) Edges() []ent.Edge {
	return []ent.Edge{
		// Required: belongs to a playlist
		edge.From("playlist", Playlist.Type).
			Ref("tracks").
			Unique().
			Required(),
		// Optional: linked to enriched catalog entries
		edge.From("artist", Artist.Type).
			Ref("playlist_tracks").
			Unique(),
		edge.From("album", Album.Type).
			Ref("playlist_tracks").
			Unique(),
		edge.From("track", Track.Type).
			Ref("playlist_tracks").
			Unique(),
	}
}

// Indexes of the PlaylistTrack.
func (PlaylistTrack) Indexes() []ent.Index {
	return []ent.Index{
		// Unique constraint: same track can only appear once at a position in a playlist
		index.Fields("position").
			Edges("playlist").
			Unique(),
		// Allow lookup by remote_id within a playlist
		index.Fields("remote_id").
			Edges("playlist"),
	}
}
