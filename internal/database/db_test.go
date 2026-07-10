package database

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestNewClient_Success verifies that NewClient opens a connection, runs
// migrations, and emits structured connection/migration logs through the
// injected logger.
// Governing: ADR-0010 (slog structured logging), SPEC-0016 REQ "Schema Migration"
func TestNewClient_Success(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	client, err := NewClient(context.Background(), "sqlite3", "file:db_test_success?mode=memory&cache=shared&_fk=1", nil, logger)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer func() {
		if cerr := client.Close(); cerr != nil {
			t.Errorf("failed to close client: %v", cerr)
		}
	}()

	logs := buf.String()
	for _, want := range []string{
		"database connection opened",
		"running schema migration",
		"schema migration complete",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("expected log output to contain %q, got:\n%s", want, logs)
		}
	}
}

// TestNewClient_ContextCancelled verifies that the ctx parameter is threaded
// through to migration queries and that failures are wrapped with %w so
// callers can inspect them with errors.Is.
func TestNewClient_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	client, err := NewClient(ctx, "sqlite3", "file:db_test_cancelled?mode=memory&cache=shared&_fk=1", nil, logger)
	if err == nil {
		_ = client.Close()
		t.Fatal("expected NewClient to fail with a cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected error chain to contain context.Canceled (via %%w wrapping), got: %v", err)
	}
}

// TestNewClient_NilLogger verifies that a nil logger falls back to
// slog.Default instead of panicking.
func TestNewClient_NilLogger(t *testing.T) {
	client, err := NewClient(context.Background(), "sqlite3", "file:db_test_nillogger?mode=memory&cache=shared&_fk=1", nil, nil)
	if err != nil {
		t.Fatalf("NewClient with nil logger failed: %v", err)
	}
	if cerr := client.Close(); cerr != nil {
		t.Errorf("failed to close client: %v", cerr)
	}
}
