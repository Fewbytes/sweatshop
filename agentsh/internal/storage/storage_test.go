package storage

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"
)

func TestInvocationLifecycleAndFTS(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "history.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	started := time.Now().UTC().Truncate(time.Microsecond)
	original := Invocation{ID: "inv_12345678", Session: "default", Argv: []string{"bash", "-c", "echo needle"}, Command: "echo needle", CWD: "/tmp", State: StateRunning, StartedAt: started}
	if err := store.CreateInvocation(ctx, original); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPID(ctx, original.ID, 12345); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetInvocation(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID == nil || *got.PID != 12345 || got.State != StateRunning {
		t.Fatalf("unexpected invocation: %+v", got)
	}

	ended := started.Add(time.Second)
	exit := 0
	reason := "ok"
	got.State, got.EndedAt, got.ExitCode, got.Reason, got.DurationMS = StateExited, &ended, &exit, &reason, 1000
	got.Stdout = StreamRef{SHA256: "abc", Bytes: 6, Lines: 1}
	if err := store.FinishInvocation(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetInvocation(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateExited || got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("unexpected terminal invocation: %+v", got)
	}

	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM invocation_commands WHERE invocation_commands MATCH 'needle'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("FTS count = %d", count)
	}
}

func TestReconcileMarksDeadRunningInvocation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "history.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pid := 999
	inv := Invocation{ID: "inv_deadbeef", Session: "default", PID: &pid, Argv: []string{"bash", "-c", "sleep 1"}, Command: "sleep 1", CWD: "/tmp", State: StateRunning}
	if err := store.CreateInvocation(ctx, inv); err != nil {
		t.Fatal(err)
	}
	count, err := store.Reconcile(ctx, func(int) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("reconciled %d records", count)
	}
	got, err := store.GetInvocation(ctx, inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateDaemonLost || got.EndedAt == nil {
		t.Fatalf("unexpected reconciled invocation: %+v", got)
	}
}

func TestBlobStoreSeparatesAndDeduplicatesStreams(t *testing.T) {
	store := BlobStore{Root: t.TempDir()}
	write := func(content string) StreamRef {
		writer, err := store.NewWriter()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(writer, bytes.NewBufferString(content)); err != nil {
			t.Fatal(err)
		}
		ref, err := writer.Commit()
		if err != nil {
			t.Fatal(err)
		}
		return ref
	}
	stdout := write("out\n")
	stderr := write("err\n")
	duplicate := write("out\n")
	if stdout.SHA256 == stderr.SHA256 {
		t.Fatal("stdout and stderr unexpectedly share content")
	}
	if stdout.SHA256 != duplicate.SHA256 {
		t.Fatal("equal content did not deduplicate")
	}
	file, err := store.Open(stdout.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, _ := io.ReadAll(file)
	if string(data) != "out\n" || stdout.Bytes != 4 || stdout.Lines != 1 {
		t.Fatalf("unexpected blob: %q %+v", data, stdout)
	}
}

func TestLogTemplatesStorageAndPriorLookup(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "history.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	inv1 := Invocation{ID: "inv_t1", Session: "default", Command: "run_worker", CWD: "/app", State: StateRunning}
	if err := store.CreateInvocation(ctx, inv1); err != nil {
		t.Fatal(err)
	}
	ended := time.Now().UTC()
	inv1.State = StateExited
	inv1.EndedAt = &ended
	if err := store.FinishInvocation(ctx, inv1); err != nil {
		t.Fatal(err)
	}

	templates := []StoredLogTemplate{
		{InvocationID: "inv_t1", Stream: "stdout", TemplateID: "tmpl_ping", Template: "Ping <IP>", Count: 50, FirstLine: 1, LastLine: 50, Level: "INFO"},
		{InvocationID: "inv_t1", Stream: "stdout", TemplateID: "tmpl_warn", Template: "High latency: <NUM>ms", Count: 3, FirstLine: 5, LastLine: 40, Level: "WARN"},
	}

	if err := store.SaveLogTemplates(ctx, "inv_t1", "stdout", templates); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetLogTemplates(ctx, "inv_t1", "stdout")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("GetLogTemplates returned %d templates, want 2", len(got))
	}

	// Test prior template frequency aggregation
	priorCounts, priorRuns, err := store.GetPriorTemplatesForCommand(ctx, "run_worker", "inv_t2")
	if err != nil {
		t.Fatal(err)
	}
	if priorRuns != 1 {
		t.Errorf("priorRuns = %d, want 1", priorRuns)
	}
	if priorCounts["tmpl_ping"] != 50 || priorCounts["tmpl_warn"] != 3 {
		t.Errorf("unexpected prior counts: %+v", priorCounts)
	}
}
