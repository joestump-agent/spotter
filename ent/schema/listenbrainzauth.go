// Governing: SPEC music-provider-integration REQ "ListenBrainz Provider" (REQ-PROV-046),
// ADR-0006 (token encrypted at rest via database hooks)
package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"

	spmixin "spotter/ent/schema/mixin"
)

// ListenBrainzAuth holds the schema definition for the ListenBrainzAuth entity.
// ListenBrainz uses a static per-user token (pasted from
// listenbrainz.org/settings) instead of an OAuth flow.
type ListenBrainzAuth struct {
	ent.Schema
}

// Mixin of the ListenBrainzAuth.
func (ListenBrainzAuth) Mixin() []ent.Mixin {
	return []ent.Mixin{
		spmixin.Timestamps{},
	}
}

// Fields of the ListenBrainzAuth.
func (ListenBrainzAuth) Fields() []ent.Field {
	return []ent.Field{
		field.Time("last_synced_at").
			Optional(),
		// Governing: ADR-0006 — encrypted at rest by encryptListenBrainzAuthHook
		// in internal/database/hooks.go.
		field.String("token").
			Sensitive(),
		field.String("username"),
		// Governing: SPEC music-provider-integration REQ "ListenBrainz Listen
		// Submission" (REQ-PROV-049) — submission is opt-in and defaults OFF.
		// Default(false) is a constant default, so Ent auto-migration emits
		// `ADD COLUMN ... NOT NULL DEFAULT false`, which is safe on databases
		// that already contain rows (unlike the Go-side-only time.Now default
		// that caused the PR #39 migration failure).
		field.Bool("submit_listens").
			Default(false).
			Comment("Opt-in: push listens from other sources to ListenBrainz"),
	}
}

// Edges of the ListenBrainzAuth.
func (ListenBrainzAuth) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("listenbrainz_auth").
			Unique().
			Required(),
	}
}
