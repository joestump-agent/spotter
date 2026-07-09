package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"spotter/internal/crypto"

	_ "github.com/mattn/go-sqlite3"
)

// testDB creates a temporary SQLite database with the auth tables and returns
// the db handle and a cleanup function.
func testDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	f, err := os.CreateTemp("", "spotter-rotate-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp db: %v", err)
	}
	path := f.Name()
	f.Close()

	dsn := "file:" + path + "?cache=shared&_fk=1"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		os.Remove(path)
		t.Fatalf("failed to open db: %v", err)
	}

	// Create the tables matching Ent schema.
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS navidrome_auths (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			password TEXT NOT NULL DEFAULT '',
			last_synced_at DATETIME,
			user_navidrome_auth INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS spotify_auths (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			display_name TEXT,
			last_synced_at DATETIME,
			access_token TEXT NOT NULL DEFAULT '',
			refresh_token TEXT NOT NULL DEFAULT '',
			expiry DATETIME,
			user_spotify_auth INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS last_fm_auths (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			last_synced_at DATETIME,
			session_key TEXT NOT NULL DEFAULT '',
			username TEXT NOT NULL DEFAULT '',
			user_lastfm_auth INTEGER
		)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			db.Close()
			os.Remove(path)
			t.Fatalf("failed to create table: %v", err)
		}
	}

	t.Cleanup(func() {
		db.Close()
		os.Remove(path)
	})

	return db, dsn
}

func randomHexKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("failed to generate random key: %v", err)
	}
	return hex.EncodeToString(key)
}

func mustEncryptor(t *testing.T, hexKey string) *crypto.Encryptor {
	t.Helper()
	keyBytes, err := hex.DecodeString(hexKey)
	if err != nil {
		t.Fatalf("bad hex key: %v", err)
	}
	enc, err := crypto.NewEncryptor(keyBytes)
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}
	return enc
}

func TestParseHexKey(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid lowercase", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", false},
		{"valid uppercase", "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF", false},
		{"valid mixed", "0123456789AbCdEf0123456789aBcDeF0123456789AbCdEf0123456789aBcDeF", false},
		{"too short", "0123456789abcdef", true},
		{"too long", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef00", true},
		{"non-hex chars", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeg", true},
		{"empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseHexKey(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseHexKey() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRunIdenticalKeys(t *testing.T) {
	key := randomHexKey(t)
	_, dsn := testDB(t)
	err := run(key, key, "sqlite3", dsn)
	if err == nil {
		t.Fatal("expected error for identical keys")
	}
	if err.Error() != "old key and new key must not be identical" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunNoEncryptedFields(t *testing.T) {
	oldKey := randomHexKey(t)
	newKey := randomHexKey(t)
	_, dsn := testDB(t)

	// No rows in any table => should warn and exit cleanly.
	err := run(oldKey, newKey, "sqlite3", dsn)
	if err != nil {
		t.Fatalf("expected no error for empty database, got: %v", err)
	}
}

func TestRunFullRotation(t *testing.T) {
	oldKeyHex := randomHexKey(t)
	newKeyHex := randomHexKey(t)
	db, dsn := testDB(t)

	oldEnc := mustEncryptor(t, oldKeyHex)

	// Insert encrypted data using old key.
	navPass, err := oldEnc.Encrypt("navidrome-password")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	spotAccess, err := oldEnc.Encrypt("spotify-access-token")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	spotRefresh, err := oldEnc.Encrypt("spotify-refresh-token")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	lastFMKey, err := oldEnc.Encrypt("lastfm-session-key")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	if _, err := db.Exec("INSERT INTO navidrome_auths (password) VALUES (?)", navPass); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := db.Exec("INSERT INTO spotify_auths (access_token, refresh_token) VALUES (?, ?)", spotAccess, spotRefresh); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := db.Exec("INSERT INTO last_fm_auths (session_key, username) VALUES (?, ?)", lastFMKey, "testuser"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Run rotation.
	if err := run(oldKeyHex, newKeyHex, "sqlite3", dsn); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	// Verify all fields decrypt with new key.
	newEnc := mustEncryptor(t, newKeyHex)

	var encVal string
	db.QueryRow("SELECT password FROM navidrome_auths WHERE id = 1").Scan(&encVal)
	dec, err := newEnc.Decrypt(encVal)
	if err != nil {
		t.Fatalf("decrypt navidrome password with new key: %v", err)
	}
	if dec != "navidrome-password" {
		t.Errorf("navidrome password = %q, want %q", dec, "navidrome-password")
	}

	db.QueryRow("SELECT access_token FROM spotify_auths WHERE id = 1").Scan(&encVal)
	dec, err = newEnc.Decrypt(encVal)
	if err != nil {
		t.Fatalf("decrypt spotify access_token with new key: %v", err)
	}
	if dec != "spotify-access-token" {
		t.Errorf("spotify access_token = %q, want %q", dec, "spotify-access-token")
	}

	db.QueryRow("SELECT refresh_token FROM spotify_auths WHERE id = 1").Scan(&encVal)
	dec, err = newEnc.Decrypt(encVal)
	if err != nil {
		t.Fatalf("decrypt spotify refresh_token with new key: %v", err)
	}
	if dec != "spotify-refresh-token" {
		t.Errorf("spotify refresh_token = %q, want %q", dec, "spotify-refresh-token")
	}

	db.QueryRow("SELECT session_key FROM last_fm_auths WHERE id = 1").Scan(&encVal)
	dec, err = newEnc.Decrypt(encVal)
	if err != nil {
		t.Fatalf("decrypt lastfm session_key with new key: %v", err)
	}
	if dec != "lastfm-session-key" {
		t.Errorf("lastfm session_key = %q, want %q", dec, "lastfm-session-key")
	}

	// Verify old key can no longer decrypt.
	db.QueryRow("SELECT password FROM navidrome_auths WHERE id = 1").Scan(&encVal)
	_, err = oldEnc.Decrypt(encVal)
	if err == nil {
		t.Error("old key should NOT be able to decrypt rotated data")
	}
}

func TestRunWrongOldKey(t *testing.T) {
	correctKeyHex := randomHexKey(t)
	wrongKeyHex := randomHexKey(t)
	newKeyHex := randomHexKey(t)
	db, dsn := testDB(t)

	correctEnc := mustEncryptor(t, correctKeyHex)

	// Insert data encrypted with the correct key.
	navPass, _ := correctEnc.Encrypt("secret")
	if _, err := db.Exec("INSERT INTO navidrome_auths (password) VALUES (?)", navPass); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Try to rotate with the wrong old key.
	err := run(wrongKeyHex, newKeyHex, "sqlite3", dsn)
	if err == nil {
		t.Fatal("expected error when old key is wrong")
	}
}

func TestRunMultipleRows(t *testing.T) {
	oldKeyHex := randomHexKey(t)
	newKeyHex := randomHexKey(t)
	db, dsn := testDB(t)

	oldEnc := mustEncryptor(t, oldKeyHex)

	// Insert multiple rows.
	for i := 0; i < 5; i++ {
		enc, _ := oldEnc.Encrypt("password-" + string(rune('A'+i)))
		if _, err := db.Exec("INSERT INTO navidrome_auths (password) VALUES (?)", enc); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	if err := run(oldKeyHex, newKeyHex, "sqlite3", dsn); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	// Verify all rows.
	newEnc := mustEncryptor(t, newKeyHex)
	rows, err := db.Query("SELECT id, password FROM navidrome_auths")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id int
		var encVal string
		if err := rows.Scan(&id, &encVal); err != nil {
			t.Fatalf("scan: %v", err)
		}
		dec, err := newEnc.Decrypt(encVal)
		if err != nil {
			t.Fatalf("decrypt row %d: %v", id, err)
		}
		expected := "password-" + string(rune('A'+count))
		if dec != expected {
			t.Errorf("row %d: got %q, want %q", id, dec, expected)
		}
		count++
	}
	if count != 5 {
		t.Errorf("expected 5 rows, got %d", count)
	}
}

func TestRunSkipsEmptyFields(t *testing.T) {
	oldKeyHex := randomHexKey(t)
	newKeyHex := randomHexKey(t)
	db, dsn := testDB(t)

	oldEnc := mustEncryptor(t, oldKeyHex)

	// Insert a row with encrypted password, and another with empty password.
	enc, _ := oldEnc.Encrypt("real-password")
	if _, err := db.Exec("INSERT INTO navidrome_auths (password) VALUES (?)", enc); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := db.Exec("INSERT INTO navidrome_auths (password) VALUES ('')"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := run(oldKeyHex, newKeyHex, "sqlite3", dsn); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	// Empty field should stay empty.
	var emptyVal string
	db.QueryRow("SELECT password FROM navidrome_auths WHERE id = 2").Scan(&emptyVal)
	if emptyVal != "" {
		t.Errorf("expected empty password for row 2, got %q", emptyVal)
	}
}

// TestRunLegacyCiphertextRotation verifies that legacy (bare base64)
// ciphertexts written before the enc:v1: marker still rotate, and that the
// rotated values are written in the new enc:v1: format.
func TestRunLegacyCiphertextRotation(t *testing.T) {
	oldKeyHex := randomHexKey(t)
	newKeyHex := randomHexKey(t)
	db, dsn := testDB(t)

	oldEnc := mustEncryptor(t, oldKeyHex)

	// Simulate a pre-marker ciphertext by stripping the enc:v1: prefix.
	enc, err := oldEnc.Encrypt("legacy-password")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	legacy := strings.TrimPrefix(enc, crypto.EncPrefixV1)
	if _, err := db.Exec("INSERT INTO navidrome_auths (password) VALUES (?)", legacy); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := run(oldKeyHex, newKeyHex, "sqlite3", dsn); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	var encVal string
	db.QueryRow("SELECT password FROM navidrome_auths WHERE id = 1").Scan(&encVal)
	if !crypto.IsEncrypted(encVal) {
		t.Errorf("rotated value should carry the %s marker, got %q", crypto.EncPrefixV1, encVal)
	}

	newEnc := mustEncryptor(t, newKeyHex)
	dec, err := newEnc.Decrypt(encVal)
	if err != nil {
		t.Fatalf("decrypt with new key: %v", err)
	}
	if dec != "legacy-password" {
		t.Errorf("password = %q, want %q", dec, "legacy-password")
	}
}

// TestRunSkipsPlaintextValues verifies that plaintext values — including
// plaintext that merely looks like base64 — are left untouched by rotation
// instead of failing the whole run.
func TestRunSkipsPlaintextValues(t *testing.T) {
	oldKeyHex := randomHexKey(t)
	newKeyHex := randomHexKey(t)
	db, dsn := testDB(t)

	oldEnc := mustEncryptor(t, oldKeyHex)

	enc, err := oldEnc.Encrypt("real-password")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := db.Exec("INSERT INTO navidrome_auths (password) VALUES (?)", enc); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Base64-shaped plaintext: the old heuristic misclassified this as ciphertext.
	base64Plaintext := "dGhpcyBpcyBqdXN0IGEgbG9uZyBwbGFpbnRleHQgdmFsdWU="
	if _, err := db.Exec("INSERT INTO navidrome_auths (password) VALUES (?)", base64Plaintext); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := run(oldKeyHex, newKeyHex, "sqlite3", dsn); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	// Encrypted row rotated to the new key.
	var encVal string
	db.QueryRow("SELECT password FROM navidrome_auths WHERE id = 1").Scan(&encVal)
	newEnc := mustEncryptor(t, newKeyHex)
	dec, err := newEnc.Decrypt(encVal)
	if err != nil {
		t.Fatalf("decrypt with new key: %v", err)
	}
	if dec != "real-password" {
		t.Errorf("password = %q, want %q", dec, "real-password")
	}

	// Plaintext row left exactly as-is.
	var plainVal string
	db.QueryRow("SELECT password FROM navidrome_auths WHERE id = 2").Scan(&plainVal)
	if plainVal != base64Plaintext {
		t.Errorf("plaintext row modified: got %q, want %q", plainVal, base64Plaintext)
	}
}

func TestRunInvalidOldKeyFormat(t *testing.T) {
	err := run("notahexkey", randomHexKey(t), "sqlite3", "file::memory:")
	if err == nil {
		t.Fatal("expected error for invalid old key format")
	}
}

func TestRunInvalidNewKeyFormat(t *testing.T) {
	err := run(randomHexKey(t), "tooshort", "sqlite3", "file::memory:")
	if err == nil {
		t.Fatal("expected error for invalid new key format")
	}
}

func TestRunMissingKeys(t *testing.T) {
	err := run("", randomHexKey(t), "sqlite3", "file::memory:")
	if err == nil {
		t.Fatal("expected error for missing old key")
	}

	err = run(randomHexKey(t), "", "sqlite3", "file::memory:")
	if err == nil {
		t.Fatal("expected error for missing new key")
	}
}

// TestReadEncryptedRows verifies rows are fully buffered and the cursor is
// closed before the caller issues UPDATEs on the same transaction. PostgreSQL
// ("pq: conn busy") and MySQL ("commands out of sync") reject statements
// while a Rows cursor is open, so reencryptAll must read-then-write; this
// test exercises the read-then-write shape on SQLite.
// Governing: SPEC key-rotation REQ "ROT-010", ADR-0023 (multi-database support)
func TestReadEncryptedRows(t *testing.T) {
	db, _ := testDB(t)

	for i := 1; i <= 3; i++ {
		if _, err := db.Exec(
			`INSERT INTO spotify_auths (access_token, refresh_token) VALUES (?, ?)`,
			"access-"+strings.Repeat("x", i), "refresh-"+strings.Repeat("y", i)); err != nil {
			t.Fatalf("failed to seed row: %v", err)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("failed to begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := readEncryptedRows(tx, "spotify_auths", []string{"access_token", "refresh_token"})
	if err != nil {
		t.Fatalf("readEncryptedRows failed: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 buffered rows, got %d", len(rows))
	}
	for _, row := range rows {
		if len(row.vals) != 2 {
			t.Fatalf("expected 2 values per row, got %d", len(row.vals))
		}
		if !strings.HasPrefix(row.vals[0].String, "access-") || !strings.HasPrefix(row.vals[1].String, "refresh-") {
			t.Fatalf("unexpected buffered values: %+v", row.vals)
		}
	}

	// The cursor must be closed: an UPDATE on the same transaction has to
	// succeed for every buffered row.
	for _, row := range rows {
		if _, err := tx.Exec(`UPDATE spotify_auths SET access_token = ? WHERE id = ?`, "rotated", row.id); err != nil {
			t.Fatalf("update after readEncryptedRows failed (cursor still open?): %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM spotify_auths WHERE access_token = 'rotated'`).Scan(&n); err != nil {
		t.Fatalf("count failed: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 rotated rows, got %d", n)
	}
}
