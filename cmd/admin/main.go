// Governing: ADR-0021 (encryption key rotation), ADR-0006 (AES-256-GCM encryption), ADR-0023 (multi-database support), SPEC key-rotation
package main

import (
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	"spotter/internal/config"
	"spotter/internal/crypto"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
)

const driverSQLite3 = "sqlite3"

// encryptedField describes a single encrypted column in the database.
type encryptedField struct {
	table  string
	column string
}

// allEncryptedFields lists every encrypted column that must be re-encrypted.
// Governing: SPEC key-rotation REQ "ROT-011"
var allEncryptedFields = []encryptedField{
	{table: "navidrome_auths", column: "password"},
	{table: "spotify_auths", column: "access_token"},
	{table: "spotify_auths", column: "refresh_token"},
	{table: "last_fm_auths", column: "session_key"},
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "rotate-key" {
		fmt.Fprintln(os.Stderr, "Usage: spotter-admin rotate-key --old-key=<hex> --new-key=<hex> [--db=<dsn>]")
		os.Exit(1)
	}

	// Parse flags after the subcommand.
	fs := flag.NewFlagSet("rotate-key", flag.ExitOnError)
	oldKeyHex := fs.String("old-key", "", "Current 64-char hex encryption key (required)")
	newKeyHex := fs.String("new-key", "", "New 64-char hex encryption key (required)")
	dbDSNFlag := fs.String("db", "", "Database DSN (overrides SPOTTER_DATABASE_SOURCE env var)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		os.Exit(1)
	}

	// Governing: ADR-0023 (multi-database support), ADR-0009 (Viper configuration)
	// Determine database driver and DSN via config.Load() — the same Viper-backed
	// loader the server uses (SPOTTER_DATABASE_DRIVER / SPOTTER_DATABASE_SOURCE),
	// including driver validation and driver-specific default sources. The --db
	// flag still overrides the configured DSN.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to load config: %v\n", err)
		os.Exit(1)
	}
	driver := cfg.Database.Driver
	dsn := cfg.Database.Source
	if *dbDSNFlag != "" {
		dsn = *dbDSNFlag
	}

	if err := run(*oldKeyHex, *newKeyHex, driver, dsn); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(oldKeyHex, newKeyHex, driver, dbDSN string) error {
	// --- REQ "ROT-001": Validate required flags ---
	if oldKeyHex == "" {
		return fmt.Errorf("--old-key is required")
	}
	if newKeyHex == "" {
		return fmt.Errorf("--new-key is required")
	}

	// --- REQ "ROT-002", "ROT-030": Validate key format ---
	oldKeyBytes, err := parseHexKey(oldKeyHex)
	if err != nil {
		return fmt.Errorf("--old-key: %w", err)
	}
	newKeyBytes, err := parseHexKey(newKeyHex)
	if err != nil {
		return fmt.Errorf("--new-key: %w", err)
	}

	// --- REQ "ROT-003": Keys must differ ---
	if oldKeyHex == newKeyHex {
		return fmt.Errorf("old key and new key must not be identical")
	}

	// --- REQ "ROT-031": Create encryptors ---
	oldEnc, err := crypto.NewEncryptor(oldKeyBytes)
	if err != nil {
		return fmt.Errorf("invalid old key: %w", err)
	}
	newEnc, err := crypto.NewEncryptor(newKeyBytes)
	if err != nil {
		return fmt.Errorf("invalid new key: %w", err)
	}

	// --- REQ "ROT-005": Check database connectivity and lock (driver-aware) ---
	// Governing: ADR-0023 (multi-database support)
	db, err := sql.Open(driver, dbDSN)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	if driver == driverSQLite3 {
		// SQLite-specific: attempt to acquire an exclusive lock to check if the server is running.
		if _, err := db.Exec("PRAGMA locking_mode=EXCLUSIVE"); err != nil {
			return fmt.Errorf("database appears to be locked (is the server running?): %w", err)
		}
		if _, err := db.Exec("BEGIN EXCLUSIVE"); err != nil {
			return fmt.Errorf("database is locked (is the server running?): %w", err)
		}
		if _, err := db.Exec("ROLLBACK"); err != nil {
			return fmt.Errorf("failed to release test lock: %w", err)
		}
	} else {
		// PostgreSQL/MySQL: verify connectivity with a ping.
		if err := db.Ping(); err != nil {
			return fmt.Errorf("failed to connect to database: %w", err)
		}
	}

	// --- REQ "ROT-004": Pre-rotation validation ---
	found, err := verifyOldKey(db, oldEnc)
	if err != nil {
		return fmt.Errorf("pre-rotation validation failed: %w", err)
	}
	if !found {
		fmt.Println("Warning: no encrypted fields found in the database. Nothing to rotate.")
		return nil
	}

	// --- REQ "ROT-010": All re-encryption in one transaction ---
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	counts, totalFields, err := reencryptAll(tx, oldEnc, newEnc, driver)
	if err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: rollback failed: %v\n", rbErr)
		}
		return err
	}

	// --- REQ "ROT-042": Handle commit failure ---
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("COMMIT failed: %w\nPlease check database integrity before retrying", err)
	}

	// --- REQ "ROT-020", "ROT-021": Post-commit verification ---
	if err := verifyNewKey(db, newEnc); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: Verification failed after commit: %v\n", err)
		fmt.Fprintln(os.Stderr, "Consider restoring from backup and retrying.")
		return fmt.Errorf("verification failed: %w", err)
	}

	// --- REQ "ROT-050": Print summary ---
	// REQ "ROT-051": NEVER log old or new key values — only print the new key in the env instruction.
	fmt.Println("Key rotation complete.")
	fmt.Printf("  NavidromeAuth: %d rows re-encrypted\n", counts["navidrome_auths"])
	fmt.Printf("  SpotifyAuth:   %d rows re-encrypted (access_token + refresh_token)\n", counts["spotify_auths"])
	fmt.Printf("  LastFMAuth:    %d rows re-encrypted\n", counts["last_fm_auths"])
	fmt.Printf("  Total fields:  %d\n", totalFields)
	fmt.Println("  Verification:  PASSED")
	fmt.Println()
	fmt.Println("Update your environment variable:")
	fmt.Printf("  SPOTTER_SECURITY_ENCRYPTION_KEY=%s\n", newKeyHex)

	return nil
}

// parseHexKey validates a hex key string and converts it to a 32-byte slice.
// Governing: SPEC key-rotation REQ "ROT-002", REQ "ROT-030", REQ "ROT-031"
func parseHexKey(hexKey string) ([]byte, error) {
	if len(hexKey) != 64 {
		return nil, fmt.Errorf("key must be exactly 64 hex characters, got %d", len(hexKey))
	}
	for _, c := range hexKey {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return nil, fmt.Errorf("key must contain only hexadecimal characters [0-9a-fA-F]")
		}
	}
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decode hex key: %w", err)
	}
	return key, nil
}

// verifyOldKey attempts to decrypt one encrypted field with the old key.
// Returns (true, nil) if at least one field was found and decrypted successfully.
// Returns (false, nil) if no encrypted fields exist at all.
// Governing: SPEC key-rotation REQ "ROT-004"
func verifyOldKey(db *sql.DB, oldEnc *crypto.Encryptor) (bool, error) {
	for _, f := range allEncryptedFields {
		query := fmt.Sprintf("SELECT id, %s FROM %s LIMIT 1", f.column, f.table)
		rows, err := db.Query(query)
		if err != nil {
			return false, fmt.Errorf("failed to query %s.%s: %w", f.table, f.column, err)
		}

		if rows.Next() {
			var id int
			var val sql.NullString
			if err := rows.Scan(&id, &val); err != nil {
				_ = rows.Close()
				return false, fmt.Errorf("failed to scan %s.%s: %w", f.table, f.column, err)
			}
			_ = rows.Close()

			if val.Valid && val.String != "" {
				_, err := oldEnc.Decrypt(val.String)
				if err != nil {
					return false, fmt.Errorf("old key cannot decrypt %s.%s (row id=%d): %w", f.table, f.column, id, err)
				}
				return true, nil
			}
		} else {
			_ = rows.Close()
		}
	}
	return false, nil
}

// reencryptAll re-encrypts all encrypted fields in a single transaction.
// Returns per-table row counts and total field count.
// Governing: SPEC key-rotation REQ "ROT-010", REQ "ROT-011", REQ "ROT-012", REQ "ROT-013", ADR-0023 (multi-database support)
func reencryptAll(tx *sql.Tx, oldEnc, newEnc *crypto.Encryptor, driver string) (map[string]int, int, error) {
	counts := make(map[string]int)
	totalFields := 0

	// Group fields by table to count rows per table (not per field).
	type tableFields struct {
		table   string
		columns []string
	}
	grouped := make(map[string]*tableFields)
	for _, f := range allEncryptedFields {
		if _, ok := grouped[f.table]; !ok {
			grouped[f.table] = &tableFields{table: f.table}
		}
		grouped[f.table].columns = append(grouped[f.table].columns, f.column)
	}

	for _, tf := range grouped {
		cols := strings.Join(tf.columns, ", ")
		query := fmt.Sprintf("SELECT id, %s FROM %s", cols, tf.table)
		rows, err := tx.Query(query)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to query %s: %w", tf.table, err)
		}

		rowCount := 0
		for rows.Next() {
			// Build scan destinations: id + N columns.
			scanDest := make([]interface{}, 1+len(tf.columns))
			var id int
			scanDest[0] = &id
			vals := make([]sql.NullString, len(tf.columns))
			for i := range vals {
				scanDest[i+1] = &vals[i]
			}

			if err := rows.Scan(scanDest...); err != nil {
				_ = rows.Close()
				return nil, 0, fmt.Errorf("failed to scan row from %s: %w", tf.table, err)
			}

			rowModified := false
			for i, col := range tf.columns {
				if !vals[i].Valid || vals[i].String == "" {
					continue // REQ "ROT-012": skip null/empty
				}

				// REQ "ROT-040": Decrypt with old key
				plaintext, err := oldEnc.Decrypt(vals[i].String)
				if err != nil {
					_ = rows.Close()
					return nil, 0, fmt.Errorf("decryption failed for %s.%s (row id=%d): %w", tf.table, col, id, err)
				}

				// REQ "ROT-041": Encrypt with new key
				newCipher, err := newEnc.Encrypt(plaintext)
				if err != nil {
					_ = rows.Close()
					return nil, 0, fmt.Errorf("encryption failed for %s.%s (row id=%d): %w", tf.table, col, id, err)
				}

				var updateSQL string
				if driver == "postgres" {
					updateSQL = fmt.Sprintf("UPDATE %s SET %s = $1 WHERE id = $2", tf.table, col)
				} else {
					updateSQL = fmt.Sprintf("UPDATE %s SET %s = ? WHERE id = ?", tf.table, col)
				}
				if _, err := tx.Exec(updateSQL, newCipher, id); err != nil {
					_ = rows.Close()
					return nil, 0, fmt.Errorf("update failed for %s.%s (row id=%d): %w", tf.table, col, id, err)
				}
				totalFields++
				rowModified = true
			}
			if rowModified {
				rowCount++
			}
		}
		if err := rows.Err(); err != nil {
			return nil, 0, fmt.Errorf("error iterating %s: %w", tf.table, err)
		}
		_ = rows.Close()
		counts[tf.table] = rowCount
	}

	return counts, totalFields, nil
}

// verifyNewKey reads all encrypted fields from the database and verifies
// they can be decrypted with the new key.
// Governing: SPEC key-rotation REQ "ROT-020", REQ "ROT-021"
func verifyNewKey(db *sql.DB, newEnc *crypto.Encryptor) error {
	for _, f := range allEncryptedFields {
		query := fmt.Sprintf("SELECT id, %s FROM %s", f.column, f.table)
		rows, err := db.Query(query)
		if err != nil {
			return fmt.Errorf("verification query failed for %s.%s: %w", f.table, f.column, err)
		}

		for rows.Next() {
			var id int
			var val sql.NullString
			if err := rows.Scan(&id, &val); err != nil {
				_ = rows.Close()
				return fmt.Errorf("verification scan failed for %s.%s: %w", f.table, f.column, err)
			}
			if !val.Valid || val.String == "" {
				continue
			}
			if _, err := newEnc.Decrypt(val.String); err != nil {
				_ = rows.Close()
				return fmt.Errorf("verification decrypt failed for %s.%s (row id=%d): %w", f.table, f.column, id, err)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("verification iteration failed for %s.%s: %w", f.table, f.column, err)
		}
		_ = rows.Close()
	}
	return nil
}
