# Encryption Key Rotation via Admin Subcommand

**Status:** accepted
**Version:** 0.1.0
**Last Updated:** 2026-02-21
**Governing ADRs:** ADR-0021 (encryption key rotation), ADR-0006 (AES-256-GCM application-layer encryption), ADR-0023 (multi-database support: PostgreSQL, MySQL, SQLite)

## Overview

This spec defines the `spotter admin rotate-key` subcommand that atomically re-encrypts all sensitive credential fields in the database from an old encryption key to a new one. The operation runs offline (server must be stopped), executes within a single database transaction, and includes a post-commit verification step. The subcommand supports PostgreSQL, MySQL, and SQLite via the `SPOTTER_DATABASE_DRIVER` and `SPOTTER_DATABASE_SOURCE` environment variables.

## Scope

This spec covers:
- CLI interface and flag validation
- Pre-rotation checks (server not running, old key validation)
- Transaction-scoped re-encryption of all encrypted fields
- Post-commit verification
- Error handling and rollback behavior
- Audit logging (stdout)

Out of scope: Automated key generation, key storage recommendations (documented in deployment guides), runtime key hot-swap, encrypted field discovery (fields are enumerated statically).

---

## Requirements

### Pre-rotation Validation

**REQ-ROT-001** — The `rotate-key` subcommand MUST accept the following flags:
- `--old-key` (required): The current 64-character hex encryption key
- `--new-key` (required): The new 64-character hex encryption key
- `--db` (optional): Database DSN (overrides `SPOTTER_DATABASE_SOURCE` env var; defaults to `file:spotter.db?cache=shared&_fk=1` for SQLite)

**REQ-ROT-002** — Both `--old-key` and `--new-key` MUST be validated as exactly 64 hexadecimal characters (representing 32 bytes for AES-256). The subcommand MUST exit with a non-zero status and descriptive error if validation fails.

**REQ-ROT-003** — The `--old-key` and `--new-key` MUST NOT be identical. The subcommand MUST exit with an error if they match.

**REQ-ROT-004** — Before modifying any data, the subcommand MUST attempt to decrypt at least one encrypted field with the `--old-key` to verify the key is correct. If no encrypted fields exist in the database, the subcommand MUST print a warning and exit successfully.

**REQ-ROT-005** — The subcommand MUST verify database availability before proceeding. For SQLite, it MUST check that the database is not locked by another process. For PostgreSQL and MySQL, it MUST verify connectivity with a ping. If the check fails, the subcommand MUST exit with an error instructing the operator to stop the server first.

### Transaction Atomicity

**REQ-ROT-010** — All re-encryption operations MUST execute within a single database transaction (`BEGIN ... COMMIT`). If any operation fails, the entire transaction MUST be rolled back.

**REQ-ROT-011** — The subcommand MUST re-encrypt the following fields:

| Entity | Table | Field(s) |
|--------|-------|----------|
| `NavidromeAuth` | `navidrome_auths` | `password` |
| `SpotifyAuth` | `spotify_auths` | `access_token`, `refresh_token` |
| `LastFMAuth` | `last_fm_auths` | `session_key` |

**REQ-ROT-012** — For each row in each table, the subcommand MUST:
1. Read the encrypted field value
2. Skip empty/null values (no encryption needed)
3. Decrypt the value using the old key via `crypto.Encryptor.Decrypt()`
4. Re-encrypt the plaintext using the new key via `crypto.Encryptor.Encrypt()`
5. Update the row with the new ciphertext

**REQ-ROT-013** — The subcommand MUST NOT use Ent ORM hooks for the rotation operation. It MUST operate on raw SQL or use Ent without hooks to avoid double-encryption (the hooks would attempt to encrypt an already-encrypted value).

### Verification Step

**REQ-ROT-020** — After the transaction is committed, the subcommand MUST perform a verification pass:
1. Create a new `Encryptor` with the new key
2. Read all encrypted fields from the database
3. Attempt to decrypt each field with the new key
4. If any decryption fails, print a clear error message advising the operator to restore from backup

**REQ-ROT-021** — The verification step MUST read directly from the database (not from cached values) to confirm the committed data is correct.

### Key Format Validation

**REQ-ROT-030** — The key format validation MUST match the existing validation in `config.Load()`: exactly 64 characters, all hexadecimal (`[0-9a-fA-F]`).

**REQ-ROT-031** — The subcommand MUST convert the hex key to a 32-byte slice using the same logic as `config.GetEncryptionKeyBytes()`.

### Rollback on Failure

**REQ-ROT-040** — If decryption with the old key fails for any field, the transaction MUST be rolled back and the subcommand MUST exit with a non-zero status and a message indicating which field and row failed.

**REQ-ROT-041** — If encryption with the new key fails for any field, the transaction MUST be rolled back identically.

**REQ-ROT-042** — If the `COMMIT` itself fails (e.g., disk full), the subcommand MUST report the error and advise checking database integrity.

### Audit Logging

**REQ-ROT-050** — The subcommand MUST print a summary to stdout upon successful completion:
```text
Key rotation complete.
  NavidromeAuth: N rows re-encrypted
  SpotifyAuth:   N rows re-encrypted (access_token + refresh_token)
  LastFMAuth:    N rows re-encrypted
  Total fields:  N
  Verification:  PASSED

Update your environment variable:
  SPOTTER_SECURITY_ENCRYPTION_KEY=<new-key>
```

**REQ-ROT-051** — The subcommand MUST NOT log the old or new key values to stdout or any log file.

---

## Scenarios

### Scenario 1: Successful key rotation

```gherkin
Given the database contains:
  - 1 NavidromeAuth with an encrypted password
  - 1 SpotifyAuth with encrypted access_token and refresh_token
  - 1 LastFMAuth with an encrypted session_key
And the server is stopped
When the operator runs:
  spotter admin rotate-key --old-key=<current> --new-key=<new>
Then all 4 fields are decrypted with the old key
And all 4 fields are re-encrypted with the new key
And the changes are committed in a single transaction
And verification confirms the new key can decrypt all fields
And the summary is printed to stdout
```

### Scenario 2: Wrong old key provided

```gherkin
Given the database contains encrypted fields
When the operator runs rotate-key with an incorrect --old-key
Then the pre-rotation validation attempts to decrypt a field
And decryption fails (GCM authentication error)
And the subcommand prints: "Error: old key cannot decrypt existing data. Verify the key and try again."
And no database modifications are made
And the exit status is non-zero
```

### Scenario 3: Transaction rollback on partial failure

```gherkin
Given the database contains encrypted fields
And one field contains corrupted ciphertext (e.g., truncated base64)
When the operator runs rotate-key with the correct --old-key
Then decryption of the corrupted field fails
And the transaction is rolled back
And no fields are modified
And the subcommand reports which field and row ID failed
```

### Scenario 4: Server is running during rotation attempt

```gherkin
Given the Spotter server is running (holding a database connection)
When the operator runs rotate-key
Then the subcommand detects the database lock
And prints: "Error: database is locked. Stop the Spotter server before rotating keys."
And exits with non-zero status
```

### Scenario 5: Empty database (no encrypted fields)

```gherkin
Given the database has no NavidromeAuth, SpotifyAuth, or LastFMAuth rows
When the operator runs rotate-key
Then the subcommand prints: "No encrypted fields found. Nothing to rotate."
And exits with zero status
```

---

## Implementation Notes

- Subcommand entry: `cmd/admin/main.go` or integrated into `cmd/server/main.go` as `spotter admin rotate-key`
- Direct SQL: use `database/sql` with driver determined by `SPOTTER_DATABASE_DRIVER` env var (default: `sqlite3`, valid: `sqlite3`, `postgres`, `mysql`) to bypass Ent hooks
- Drivers: `mattn/go-sqlite3`, `lib/pq` (PostgreSQL), `go-sql-driver/mysql` — all imported as side-effect imports
- DSN: read from `SPOTTER_DATABASE_SOURCE` env var, overridden by `--db` flag. Examples:
  - SQLite: `file:spotter.db?cache=shared&_fk=1`
  - PostgreSQL: `postgresql://spotter:pass@localhost:5432/spotter?sslmode=disable`
  - MySQL: `spotter:pass@tcp(localhost:3306)/spotter`
- Encryptor reuse: instantiate two `crypto.Encryptor` instances (old key and new key)
- Table/column names: must match Ent-generated schema (check `ent/migrate/schema.go` for exact names)
- SQL placeholders: PostgreSQL uses `$1`, `$2`; SQLite and MySQL use `?`
- Governing comment: `// Governing: ADR-0021 (key rotation), ADR-0006 (AES-256-GCM encryption), ADR-0023 (multi-database support), SPEC key-rotation`
