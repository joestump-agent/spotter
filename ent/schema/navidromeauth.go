package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// NavidromeAuth holds the schema definition for the NavidromeAuth entity.
type NavidromeAuth struct {
	ent.Schema
}

// Fields of the NavidromeAuth.
func (NavidromeAuth) Fields() []ent.Field {
	return []ent.Field{
		// We need the password to sign requests (md5(password + salt))
		// Note: In production, this field should be encrypted at rest.
		field.String("password"),
	}
}

// Edges of the NavidromeAuth.
func (NavidromeAuth) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("navidrome_auth").
			Unique().
			Required(),
	}
}
