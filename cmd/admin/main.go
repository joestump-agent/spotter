// Governing: ADR-0021 (encryption key rotation), ADR-0006 (AES-256-GCM encryption), ADR-0023 (multi-database support), SPEC key-rotation
package main

import (
	"context"
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

	// Governing: ADR-0009 (Viper configuration), ADR-0023 (multi-database support)
	// Resolve the database driver and DSN through the shared config package so
	// the admin CLI honors the same SPOTTER_DATABASE_* env vars, defaults, and
	// driver validation as the server instead of reading os.Getenv directly.
	driver, dsn, err := config.LoadDatabase()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	// The --db flag overrides the resolved SPOTTER_DATABASE_SOURCE DSN.
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
		if err := checkSQLiteLock(context.Background(), db); err != nil {
			return err
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
	// Governing: SPEC key-rotation REQ "ROT-051" — NEVER print or log key
	// values. The operator supplied the new key via --new-key, so a
	// placeholder is sufficient here.
	fmt.Println("Key rotation complete.")
	fmt.Printf("  NavidromeAuth: %d rows re-encrypted\n", counts["navidrome_auths"])
	fmt.Printf("  SpotifyAuth:   %d rows re-encrypted (access_token + refresh_token)\n", counts["spotify_auths"])
	fmt.Printf("  LastFMAuth:    %d rows re-encrypted\n", counts["last_fm_auths"])
	fmt.Printf("  Total fields:  %d\n", totalFields)
	fmt.Println("  Verification:  PASSED")
	fmt.Println()
	fmt.Println("Update your environment variable:")
	fmt.Println("  SPOTTER_SECURITY_ENCRYPTION_KEY=<the --new-key value you provided>")

	return nil
}

// checkSQLiteLock probes for an exclusive lock on a SQLite database to verify
// the server is not running. All statements are issued on a single pooled
// connection — with db.Exec, the PRAGMA, BEGIN EXCLUSIVE, and ROLLBACK could
// each land on different connections, making the probe meaningless.
// Governing: SPEC key-rotation REQ "ROT-005", ADR-0023 (multi-database support)
func checkSQLiteLock(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire database connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "PRAGMA locking_mode=EXCLUSIVE"); err != nil {
		return fmt.Errorf("database appears to be locked (is the server running?): %w", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		return fmt.Errorf("database is locked (is the server running?): %w", err)
	}
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		return fmt.Errorf("failed to release test lock: %w", err)
	}

	// In EXCLUSIVE locking mode SQLite retains the lock after ROLLBACK. Reset
	// to NORMAL and touch the database once so the lock is actually released
	// before this connection returns to the pool.
	if _, err := conn.ExecContext(ctx, "PRAGMA locking_mode=NORMAL"); err != nil {
		return fmt.Errorf("failed to reset locking mode: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SELECT 1"); err != nil {
		return fmt.Errorf("failed to release test lock: %w", err)
	}
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
// Returns (true, nil) if at least one encrypted field was found and decrypted
// successfully. Returns (false, nil) if no encrypted fields exist at all
// (plaintext values are not encrypted fields and are skipped).
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
				// enc:v1: values MUST decrypt with the old key; legacy values
				// that fail to decrypt are treated as plaintext and skipped.
				_, wasEncrypted, err := oldEnc.DecryptAny(val.String)
				if err != nil {
					return false, fmt.Errorf("old key cannot decrypt %s.%s (row id=%d): %w", f.table, f.column, id, err)
				}
				if wasEncrypted {
					return true, nil
				}
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
		// Governing: ADR-0023 (multi-database support)
		// Read every (id, values) pair into memory and close the cursor
		// BEFORE issuing UPDATEs. PostgreSQL ("pq: conn busy") and MySQL
		// ("commands out of sync") reject statements on a transaction while
		// a Rows cursor is still open; SQLite tolerates it but gains nothing
		// from interleaving.
		encRows, err := readEncryptedRows(tx, tf.table, tf.columns)
		if err != nil {
			return nil, 0, err
		}

		rowCount := 0
		for _, row := range encRows {
			rowModified := false
			for i, col := range tf.columns {
				if !row.vals[i].Valid || row.vals[i].String == "" {
					continue // REQ "ROT-012": skip null/empty
				}

				// REQ "ROT-040": Decrypt with old key. Accepts both enc:v1:
				// and legacy bare-base64 ciphertexts; legacy values that fail
				// to decrypt are treated as plaintext and left untouched
				// (they will be encrypted by the server on next write).
				plaintext, wasEncrypted, err := oldEnc.DecryptAny(row.vals[i].String)
				if err != nil {
					return nil, 0, fmt.Errorf("decryption failed for %s.%s (row id=%d): %w", tf.table, col, row.id, err)
				}
				if !wasEncrypted {
					continue // plaintext value: nothing to rotate
				}

				// REQ "ROT-041": Encrypt with new key (written in enc:v1: format)
				newCipher, err := newEnc.Encrypt(plaintext)
				if err != nil {
					return nil, 0, fmt.Errorf("encryption failed for %s.%s (row id=%d): %w", tf.table, col, row.id, err)
				}

				var updateSQL string
				if driver == "postgres" {
					updateSQL = fmt.Sprintf("UPDATE %s SET %s = $1 WHERE id = $2", tf.table, col)
				} else {
					updateSQL = fmt.Sprintf("UPDATE %s SET %s = ? WHERE id = ?", tf.table, col)
				}
				if _, err := tx.Exec(updateSQL, newCipher, row.id); err != nil {
					return nil, 0, fmt.Errorf("update failed for %s.%s (row id=%d): %w", tf.table, col, row.id, err)
				}
				totalFields++
				rowModified = true
			}
			if rowModified {
				rowCount++
			}
		}
		counts[tf.table] = rowCount
	}

	return counts, totalFields, nil
}

// encryptedRow holds one row's id and encrypted column values, buffered in
// memory so the read cursor can be closed before UPDATEs are issued.
type encryptedRow struct {
	id   int
	vals []sql.NullString
}

// readEncryptedRows loads id plus the given columns for every row of table
// into memory and fully drains/closes the cursor before returning. This keeps
// the transaction free for subsequent UPDATEs on drivers that allow only one
// active statement per connection (PostgreSQL, MySQL).
// Governing: SPEC key-rotation REQ "ROT-010", ADR-0023 (multi-database support)
func readEncryptedRows(tx *sql.Tx, table string, columns []string) ([]encryptedRow, error) {
	query := fmt.Sprintf("SELECT id, %s FROM %s", strings.Join(columns, ", "), table)
	rows, err := tx.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	var result []encryptedRow
	for rows.Next() {
		row := encryptedRow{vals: make([]sql.NullString, len(columns))}
		scanDest := make([]interface{}, 1+len(columns))
		scanDest[0] = &row.id
		for i := range row.vals {
			scanDest[i+1] = &row.vals[i]
		}
		if err := rows.Scan(scanDest...); err != nil {
			return nil, fmt.Errorf("failed to scan row from %s: %w", table, err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating %s: %w", table, err)
	}
	return result, nil
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
			// Rotated values always carry the enc:v1: marker; values without
			// it were treated as plaintext, skipped during rotation, and are
			// not expected to decrypt.
			if !crypto.IsEncrypted(val.String) {
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
