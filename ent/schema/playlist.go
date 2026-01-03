package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Playlist holds the schema definition for the Playlist entity.
type Playlist struct {
	ent.Schema
}

// Fields of the Playlist.
func (Playlist) Fields() []ent.Field {
	return []ent.Field{
		field.String("remote_id"),
		field.String("name").
			NotEmpty(),
		field.String("description").
			Optional(),
		field.String("source").
			NotEmpty(), // e.g. "spotify", "navidrome"
		field.String("image_url").
			Optional().
			Comment("URL to the playlist cover art"),
		field.String("external_url").
			Optional().
			Comment("Deep link to the playlist on the provider's website"),
		field.Int("track_count").
			Default(0).
			Comment("Number of tracks in the playlist"),
		field.Int("unique_artists").
			Default(0).
			Comment("Number of unique artists in the playlist"),
		field.Int("unique_albums").
			Default(0).
			Comment("Number of unique albums in the playlist"),
		field.Time("created_at").
			Default(time.Now),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the Playlist.
func (Playlist) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("playlists").
			Unique().
			Required(),
	}
}

// Indexes of the Playlist.
func (Playlist) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("remote_id", "source").
			Edges("user").
			Unique(),
	}
}
