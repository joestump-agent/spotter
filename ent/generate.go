package ent

// The sql/execquery feature exposes ExecContext/QueryContext on *ent.Client and
// *ent.Tx so raw SQL (e.g. the denormalized entity_tags writes) can run inside
// the same transaction as Ent mutations.
// Governing: SPEC-0014 REQ "Denormalized Entity Tags Table"
//go:generate go run -mod=mod entgo.io/ent/cmd/ent generate --feature sql/execquery ./schema
