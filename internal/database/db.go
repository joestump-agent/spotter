package database

// Governing: SPEC-0016 REQ "Driver Registration", ADR-0023
import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"spotter/ent"
	"spotter/internal/crypto"

	"github.com/go-sql-driver/mysql"
	"github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
)

const driverPostgres = "postgres"

func NewClient(driver, source string, encryptor *crypto.Encryptor) (*ent.Client, error) {
	client, err := ent.Open(driver, source)
	if err != nil {
		return nil, fmt.Errorf("failed opening connection to %s: %v", driver, err)
	}

	// Register encryption/decryption hooks if encryptor is provided
	if encryptor != nil {
		RegisterEncryptionHooks(client, encryptor)
	}

	ctx := context.Background()

	// Open a raw database connection for custom migrations that run outside Ent.
	db, err := sql.Open(driverToStdlib(driver), source)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("failed opening raw db for custom migrations: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Governing: SPEC metadata-enrichment-pipeline (catalog uniqueness)
	// Merge duplicate (artist, name) Track rows BEFORE Schema.Create so the
	// unique index on tracks (name, artist_tracks) can be created over
	// pre-existing data.
	if _, err := DedupeTracks(ctx, driver, db, slog.Default()); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("failed deduplicating tracks before migration: %v", err)
	}

	// Governing: SPEC-0016 REQ "Schema Migration", ADR-0004 (Ent ORM handles DDL for all dialects)
	if err := client.Schema.Create(ctx); err != nil {
		// Attempt to close the client on schema creation failure
		_ = client.Close()
		return nil, fmt.Errorf("failed creating schema resources: %v", err)
	}

	// Governing: SPEC-0014 REQ "Denormalized Entity Tags Table"
	if err := CreateEntityTagsTable(ctx, driver, db); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("failed creating entity_tags table: %v", err)
	}

	return client, nil
}

// driverToStdlib maps Ent dialect names to database/sql driver names.
func driverToStdlib(driver string) string {
	switch driver {
	case driverPostgres:
		return driverPostgres
	case "mysql":
		return "mysql"
	default:
		return "sqlite3"
	}
}

// DriverName reports the dialect name ("postgres", "mysql", or "sqlite3") of
// the driver backing db. It lets code that only holds a *sql.DB (e.g. the
// entity_tags upsert in internal/tags) choose dialect-specific SQL without
// threading the configured driver name through every call site.
// Governing: SPEC-0016 REQ "Denormalized Entity Tags Table", ADR-0023
func DriverName(db *sql.DB) string {
	switch db.Driver().(type) {
	case *pq.Driver:
		return driverPostgres
	case *mysql.MySQLDriver:
		return "mysql"
	default:
		return "sqlite3"
	}
}

// OpenRawDB opens a persistent *sql.DB connection using the same driver/source
// as NewClient. Callers are responsible for closing the returned db.
func OpenRawDB(driver, source string) (*sql.DB, error) {
	db, err := sql.Open(driverToStdlib(driver), source)
	if err != nil {
		return nil, fmt.Errorf("failed opening raw db (%s): %w", driver, err)
	}
	return db, nil
}
