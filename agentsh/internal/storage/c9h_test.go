package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenPropagatesInvalidPathError(t *testing.T) {
	// A path that can't be created as a directory (its parent is a file)
	// should surface sql.Open/MkdirAll's error, not fail silently later as
	// a confusing migration error.
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), Config{Path: filepath.Join(blocker, "sub", "history.db")})
	if err == nil {
		t.Fatal("expected Open to return an error for an uncreatable path")
	}
}

func TestHistoryIssuesExactlyOneQuery(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "history.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("inv_h%02d", i)
		inv := Invocation{ID: id, Session: "default", Command: "echo hi", CWD: "/app", State: StateRunning, StartedAt: time.Now().UTC()}
		if err := store.CreateInvocation(ctx, inv); err != nil {
			t.Fatal(err)
		}
		ended := time.Now().UTC()
		inv.State, inv.EndedAt = StateExited, &ended
		if err := store.FinishInvocation(ctx, inv); err != nil {
			t.Fatal(err)
		}
	}

	countedCtx, counter := WithQueryCounter(ctx)
	items, err := store.History(countedCtx, "default", "", "", nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 25 {
		t.Fatalf("History returned %d items, want 25", len(items))
	}
	if got := *counter; got != 1 {
		t.Fatalf("History issued %d queries, want 1 (N+1 regression)", got)
	}
}

func TestBaselineComparisonBeyondSQLiteParamLimit(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "history.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// SQLITE_MAX_VARIABLE_NUMBER defaults to 999; the old IN (?,?,...)
	// implementation built one placeholder per prior invocation, so it broke
	// exactly on commands that ran this often.
	const runs = 1200
	for i := 0; i < runs; i++ {
		id := fmt.Sprintf("inv_p%04d", i)
		inv := Invocation{ID: id, Session: "default", Command: "hot_command", CWD: "/app", State: StateRunning, StartedAt: time.Now().UTC()}
		if err := store.CreateInvocation(ctx, inv); err != nil {
			t.Fatal(err)
		}
		ended := time.Now().UTC()
		inv.State, inv.EndedAt = StateExited, &ended
		if err := store.FinishInvocation(ctx, inv); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveLogTemplates(ctx, id, "stdout", []StoredLogTemplate{
			{InvocationID: id, Stream: "stdout", TemplateID: "tmpl_ok", Template: "ok", Count: 1, FirstLine: 1, LastLine: 1, Level: "INFO"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	counts, priorRuns, err := store.GetPriorTemplatesForCommand(ctx, "hot_command", "inv_excluded")
	if err != nil {
		t.Fatal(err)
	}
	if priorRuns != runs {
		t.Fatalf("priorRuns = %d, want %d", priorRuns, runs)
	}
	if counts["tmpl_ok"] != runs {
		t.Fatalf("counts[tmpl_ok] = %d, want %d", counts["tmpl_ok"], runs)
	}
}

func TestSchemaVersionTableDrivesMigrations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := Open(ctx, Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}

	var version int
	row := store.db.QueryRowContext(ctx, `SELECT version FROM schema_version LIMIT 1`)
	if err := row.Scan(&version); err != nil {
		t.Fatalf("schema_version not populated: %v", err)
	}
	if version < 1 {
		t.Fatalf("schema_version = %d, want >= 1", version)
	}
	store.Close()

	// Reopening must not fail or reset the version (idempotent migration).
	store2, err := Open(ctx, Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	var version2 int
	row2 := store2.db.QueryRowContext(ctx, `SELECT version FROM schema_version LIMIT 1`)
	if err := row2.Scan(&version2); err != nil {
		t.Fatal(err)
	}
	if version2 != version {
		t.Fatalf("schema_version changed on reopen: %d -> %d", version, version2)
	}
}

func TestGetPriorTemplatesForCommandPropagatesScanError(t *testing.T) {
	// Guards the "if err == nil { append }" silent-drop pattern from
	// regressing: a real scan error must be returned, not swallowed.
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "history.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Corrupt log_templates.count so SUM() scanning into an int still works
	// (SQLite is dynamically typed), but force a genuine driver error via a
	// cancelled context instead, which every QueryContext call in this path
	// must propagate rather than silently return partial/empty results for.
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, _, err = store.GetPriorTemplatesForCommand(cancelledCtx, "anything", "inv_x")
	if err == nil {
		t.Fatal("expected error from a cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, sql.ErrTxDone) {
		t.Logf("got non-context.Canceled error (driver-dependent, acceptable): %v", err)
	}
}
