package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	spmixin "spotter/ent/schema/mixin"
)

// DJ holds the schema definition for the DJ entity.
type DJ struct {
	ent.Schema
}

// Mixin of the DJ.
func (DJ) Mixin() []ent.Mixin {
	return []ent.Mixin{
		spmixin.Timestamps{},
	}
}

// Fields of the DJ.
func (DJ) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty().
			MaxLen(255).
			Comment("User-given name for this DJ"),
		field.Text("system_prompt").
			Optional().
			MaxLen(10000).
			Comment("Custom system prompt defining the DJ's personality"),
		field.Strings("genres_include").
			Optional().
			Comment("ID3 genres/tags to prioritize"),
		field.Strings("genres_exclude").
			Optional().
			Comment("ID3 genres/tags to exclude"),
		field.Strings("vibes").
			Optional().
			Comment("Emotion/adjective tags for the DJ's style"),
		field.Strings("artists_include").
			Optional().
			Comment("Artists to prioritize in recommendations"),
		field.Strings("artists_exclude").
			Optional().
			Comment("Artists to exclude from recommendations"),
	}
}

// Edges of the DJ.
func (DJ) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("djs").
			Unique().
			Required(),
		edge.To("mixtapes", Mixtape.Type),
	}
}

// Indexes of the DJ.
func (DJ) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name").
			Edges("user").
			Unique(),
	}
}
