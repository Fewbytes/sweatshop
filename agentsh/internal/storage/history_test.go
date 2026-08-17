package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryFiltersAndBlobGC(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, Config{Path: filepath.Join(root, "history.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, item := range []struct {
		id, command string
		code        int
	}{{"inv_aabbccdd", "echo alpha", 0}, {"inv_eeff0011", "false", 1}} {
		code := item.code
		reason := "ok"
		inv := Invocation{ID: item.id, Session: "default", Argv: []string{"bash", "-c", item.command}, Command: item.command, CWD: root, State: StateRunning, StartedAt: time.Now().UTC()}
		if err := store.CreateInvocation(ctx, inv); err != nil {
			t.Fatal(err)
		}
		ended := time.Now().UTC()
		inv.State, inv.EndedAt, inv.ExitCode, inv.Reason = StateExited, &ended, &code, &reason
		if err := store.FinishInvocation(ctx, inv); err != nil {
			t.Fatal(err)
		}
	}
	failed := 1
	items, err := store.History(ctx, "default", "false", "", &failed, 10)
	if err != nil || len(items) != 1 || items[0].Command != "false" {
		t.Fatalf("history filter: %v %+v", err, items)
	}

	blobs := BlobStore{Root: filepath.Join(root, "blobs")}
	writer, err := blobs.NewWriter()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write([]byte("expired"))
	ref, err := writer.Commit()
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(blobs.Root, ref.SHA256), old, old); err != nil {
		t.Fatal(err)
	}
	result, err := blobs.GC(time.Minute, 0)
	if err != nil || result.Removed != 1 {
		t.Fatalf("gc: %v %+v", err, result)
	}
}
