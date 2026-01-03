package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ArtistImage holds the schema definition for the ArtistImage entity.
type ArtistImage struct {
	ent.Schema
}

// Fields of the ArtistImage.
func (ArtistImage) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("image_type").
			Values("thumbnail", "background", "logo", "banner", "fanart").
			Comment("Type of artist image"),
		field.String("source").
			NotEmpty().
			Comment("Provider that supplied this image (fanart, spotify, lastfm, etc.)"),
		field.String("url").
			NotEmpty().
			Comment("Original URL of the image"),
		field.String("local_path").
			Optional().
			Comment("Local file path if image has been downloaded"),
		field.Int("width").
			Optional().
			Nillable().
			Comment("Image width in pixels"),
		field.Int("height").
			Optional().
			Nillable().
			Comment("Image height in pixels"),
		field.Int("likes").
			Optional().
			Nillable().
			Comment("Like count from Fanart.tv for sorting by popularity"),
		field.Bool("is_primary").
			Default(false).
			Comment("Whether this is the primary image of its type"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Edges of the ArtistImage.
func (ArtistImage) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("artist", Artist.Type).
			Ref("images").
			Unique().
			Required(),
	}
}

// Indexes of the ArtistImage.
func (ArtistImage) Indexes() []ent.Index {
	return []ent.Index{
		// Prevent duplicate images from the same source
		index.Fields("url").
			Edges("artist").
			Unique(),
		// Fast lookup by type
		index.Fields("image_type").
			Edges("artist"),
	}
}
