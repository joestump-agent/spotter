package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"

	spmixin "spotter/ent/schema/mixin"
)

// LastFMAuth holds the schema definition for the LastFMAuth entity.
type LastFMAuth struct {
	ent.Schema
}

// Mixin of the LastFMAuth.
func (LastFMAuth) Mixin() []ent.Mixin {
	return []ent.Mixin{
		spmixin.Timestamps{},
	}
}

// Fields of the LastFMAuth.
func (LastFMAuth) Fields() []ent.Field {
	return []ent.Field{
		field.Time("last_synced_at").
			Optional(),
		field.String("session_key"),
		field.String("username"),
	}
}

// Edges of the LastFMAuth.
func (LastFMAuth) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("lastfm_auth").
			Unique().
			Required(),
	}
}
