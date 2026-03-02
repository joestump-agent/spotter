package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	spmixin "spotter/ent/schema/mixin"
)

// SimilarArtist holds the schema definition for the SimilarArtist entity.
// It represents a similarity relationship between two artists.
type SimilarArtist struct {
	ent.Schema
}

// Mixin of the SimilarArtist.
func (SimilarArtist) Mixin() []ent.Mixin {
	return []ent.Mixin{
		spmixin.Timestamps{},
	}
}

// Fields of the SimilarArtist.
func (SimilarArtist) Fields() []ent.Field {
	return []ent.Field{
		field.Int("source_artist_id").
			Comment("The artist that similarities are being found for"),
		field.Int("similar_artist_id").
			Comment("The artist that is similar to the source artist"),
		field.String("provider").
			NotEmpty().
			Comment("The source of the similarity recommendation (e.g., 'OpenAI', 'LastFM')"),
		field.Float("confidence").
			Default(0.0).
			Min(0.0).
			Max(1.0).
			Comment("Confidence score for the similarity (0.0 to 1.0)"),
		field.Int("rank").
			Default(0).
			Comment("Rank of the similar artist (lower is more similar)"),
		field.Text("reason").
			Optional().
			Comment("AI-generated explanation for why these artists are similar"),
	}
}

// Edges of the SimilarArtist.
func (SimilarArtist) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("source_artist", Artist.Type).
			Field("source_artist_id").
			Unique().
			Required(),
		edge.To("similar_artist", Artist.Type).
			Field("similar_artist_id").
			Unique().
			Required(),
		edge.From("user", User.Type).
			Ref("similar_artists").
			Unique().
			Required(),
	}
}

// Indexes of the SimilarArtist.
func (SimilarArtist) Indexes() []ent.Index {
	return []ent.Index{
		// Unique constraint for source artist, similar artist, and provider
		index.Fields("source_artist_id", "similar_artist_id", "provider").
			Unique(),
		// Index for querying similar artists for a source artist
		index.Fields("source_artist_id"),
		// Index for querying by provider
		index.Fields("provider"),
		// Index for efficient user queries
		index.Edges("user"),
	}
}
