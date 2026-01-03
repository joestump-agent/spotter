package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
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
	}
}

// Edges of the Listen.
func (Listen) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("listens").
			Unique().
			Required(),
	}
}
