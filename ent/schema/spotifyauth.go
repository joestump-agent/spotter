package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// SpotifyAuth holds the schema definition for the SpotifyAuth entity.
type SpotifyAuth struct {
	ent.Schema
}

// Fields of the SpotifyAuth.
func (SpotifyAuth) Fields() []ent.Field {
	return []ent.Field{
		field.String("display_name").
			Optional(),
		field.Time("last_synced_at").
			Optional(),
		field.String("access_token"),
		field.String("refresh_token"),
		field.Time("expiry"),
	}
}

// Edges of the SpotifyAuth.
func (SpotifyAuth) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("spotify_auth").
			Unique().
			Required(),
	}
}
