package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AlbumImage holds the schema definition for the AlbumImage entity.
type AlbumImage struct {
	ent.Schema
}

// Fields of the AlbumImage.
func (AlbumImage) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("image_type").
			Values("cover_front", "cover_back", "cd_art", "booklet", "spine", "other").
			Default("cover_front").
			Comment("Type of album artwork"),
		field.String("source").
			NotEmpty().
			Comment("Provider that supplied this image (e.g., spotify, fanart, navidrome)"),
		field.String("url").
			Optional().
			Comment("Original URL of the image"),
		field.String("local_path").
			Optional().
			Comment("Local filesystem path where image is stored"),
		field.Int("width").
			Optional().
			Nillable().
			Comment("Image width in pixels"),
		field.Int("height").
			Optional().
			Nillable().
			Comment("Image height in pixels"),
		field.Int("size_bytes").
			Optional().
			Nillable().
			Comment("File size in bytes"),
		field.String("mime_type").
			Optional().
			Comment("MIME type (e.g., image/jpeg, image/png)"),
		field.Bool("is_primary").
			Default(false).
			Comment("Whether this is the primary image for the album"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Edges of the AlbumImage.
func (AlbumImage) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("album", Album.Type).
			Ref("images").
			Unique().
			Required(),
	}
}

// Indexes of the AlbumImage.
func (AlbumImage) Indexes() []ent.Index {
	return []ent.Index{
		// Quick lookup by album and type
		index.Fields("image_type").
			Edges("album"),
		// Find primary images
		index.Fields("is_primary").
			Edges("album"),
	}
}
