package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Album holds the schema definition for the Album entity.
type Album struct {
	ent.Schema
}

// Fields of the Album.
func (Album) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty(),
		field.String("sort_name").
			Optional().
			Comment("Name used for sorting, e.g., 'White Album, The'"),
		field.String("musicbrainz_id").
			Optional().
			Comment("MusicBrainz release group ID"),
		field.String("spotify_id").
			Optional().
			Comment("Spotify album ID"),
		field.String("release_date").
			Optional().
			Comment("Release date in ISO format (YYYY, YYYY-MM, or YYYY-MM-DD)"),
		field.Int("year").
			Optional().
			Comment("Release year extracted from release_date"),
		field.String("genre").
			Optional().
			Comment("Primary genre of the album"),
		field.JSON("tags", []string{}).
			Optional().
			Comment("Tags/genres from various sources"),
		field.Int("popularity").
			Optional().
			Comment("Popularity score (0-100, from Spotify)"),
		field.Int("total_tracks").
			Optional().
			Comment("Total number of tracks on the album"),
		field.String("album_type").
			Optional().
			Comment("Type: album, single, compilation, ep"),
		field.String("label").
			Optional().
			Comment("Record label"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
		field.Time("last_enriched_at").
			Optional().
			Comment("When metadata was last enriched"),
	}
}

// Edges of the Album.
func (Album) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("albums").
			Unique().
			Required(),
		edge.From("artist", Artist.Type).
			Ref("albums").
			Unique(),
		edge.To("tracks", Track.Type),
		edge.To("images", AlbumImage.Type),
		edge.To("listens", Listen.Type),
	}
}

// Indexes of the Album.
func (Album) Indexes() []ent.Index {
	return []ent.Index{
		// Unique album name per artist per user
		index.Fields("name").
			Edges("user", "artist").
			Unique(),
		// Fast lookup by external IDs
		index.Fields("musicbrainz_id"),
		index.Fields("spotify_id"),
		// Search by year
		index.Fields("year"),
	}
}
