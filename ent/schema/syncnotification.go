// Governing: SPEC-0015 REQ "Cooldown Persistence", ADR-0026
package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SyncNotification holds the schema for sync failure notification cooldowns.
// Each record tracks the last time an email notification was sent for a given
// user+provider pair, enforcing a configurable cooldown window (default 7 days).
type SyncNotification struct {
	ent.Schema
}

func (SyncNotification) Fields() []ent.Field {
	return []ent.Field{
		field.String("provider").MaxLen(50).NotEmpty(),
		field.Time("notified_at").Default(time.Now),
	}
}

func (SyncNotification) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).Ref("sync_notifications").Required().Unique(),
	}
}

func (SyncNotification) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("provider").Edges("user").Unique(),
	}
}
