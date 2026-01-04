package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Artist holds the schema definition for the Artist entity.
type Artist struct {
	ent.Schema
}

// Fields of the Artist.
func (Artist) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty().
			Comment("Artist name"),
		field.String("sort_name").
			Optional().
			Comment("Name used for sorting, e.g., 'Beatles, The'"),
		field.String("musicbrainz_id").
			Optional().
			Unique().
			Comment("MusicBrainz artist MBID"),
		field.String("spotify_id").
			Optional().
			Unique().
			Comment("Spotify artist ID"),
		field.String("lastfm_url").
			Optional().
			Comment("Last.fm artist page URL"),
		field.String("navidrome_id").
			Optional().
			Comment("Navidrome artist ID"),
		field.String("lidarr_id").
			Optional().
			Comment("Lidarr artist ID"),
		field.Text("bio").
			Optional().
			Comment("Artist biography from Last.fm or other sources"),
		field.JSON("tags", []string{}).
			Optional().
			Comment("Genre tags and social tags from various sources"),
		field.Int("popularity").
			Optional().
			Nillable().
			Comment("Popularity score from Spotify (0-100)"),
		field.Int("follower_count").
			Optional().
			Nillable().
			Comment("Follower count from Spotify"),
		field.JSON("genres", []string{}).
			Optional().
			Comment("Genre list from Spotify"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
		field.Time("last_enriched_at").
			Optional().
			Nillable().
			Comment("Last time metadata was enriched from providers"),
		// AI-generated fields
		field.Text("ai_summary").
			Optional().
			Comment("AI-generated summary of the artist"),
		field.Text("ai_biography").
			Optional().
			Comment("AI-generated biography for the artist"),
		field.JSON("ai_tags", []string{}).
			Optional().
			Comment("AI-generated tags for the artist (max 5)"),
		field.Time("last_ai_enriched_at").
			Optional().
			Nillable().
			Comment("Last time AI enrichment was performed"),
	}
}

// Edges of the Artist.
func (Artist) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("artists").
			Unique().
			Required(),
		edge.To("albums", Album.Type),
		edge.To("tracks", Track.Type),
		edge.To("images", ArtistImage.Type),
		edge.To("listens", Listen.Type),
		edge.To("playlist_tracks", PlaylistTrack.Type),
	}
}

// Indexes of the Artist.
func (Artist) Indexes() []ent.Index {
	return []ent.Index{
		// Unique artist name per user
		index.Fields("name").
			Edges("user").
			Unique(),
		// Index for MusicBrainz lookups
		index.Fields("musicbrainz_id"),
		// Index for Spotify lookups
		index.Fields("spotify_id"),
		index.Fields("lidarr_id"),
	}
}
