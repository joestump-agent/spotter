package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// User holds the schema definition for the User entity.
type User struct {
	ent.Schema
}

// Fields of the User.
func (User) Fields() []ent.Field {
	return []ent.Field{
		field.String("username").
			Unique().
			NotEmpty().
			MaxLen(255),
		field.String("email").
			Optional().
			MaxLen(320), // RFC 5321 max email length
		field.String("theme").
			Default("dark").
			MaxLen(50),
		field.Text("system_prompt").
			Optional().
			MaxLen(10000), // Reasonable limit for AI prompts
		field.Int("pagination_size").
			Default(25),
		field.Time("last_login_at").
			Default(time.Now),
	}
}

// Edges of the User.
func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("spotify_auth", SpotifyAuth.Type).
			Unique(),
		edge.To("lastfm_auth", LastFMAuth.Type).
			Unique(),
		edge.To("navidrome_auth", NavidromeAuth.Type).
			Unique(),
		edge.To("playlists", Playlist.Type),
		edge.To("listens", Listen.Type),
		edge.To("sync_events", SyncEvent.Type),
		// Catalog edges for metadata enrichment
		edge.To("artists", Artist.Type),
		edge.To("albums", Album.Type),
		// Vibes system edges
		edge.To("djs", DJ.Type),
		edge.To("mixtapes", Mixtape.Type),
		// Similar artists relationships
		edge.To("similar_artists", SimilarArtist.Type),
	}
}
