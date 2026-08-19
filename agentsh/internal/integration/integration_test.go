//go:build !short

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/agentsh/internal/executor"
	"github.com/Fewbytes/sweatshop/agentsh/internal/storage"
)

func newExecutor(t *testing.T) (*executor.Executor, string) {
	t.Helper()
	root := t.TempDir()
	store, err := storage.Open(context.Background(), storage.Config{Path: filepath.Join(root, "history.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	e := executor.New(store, storage.BlobStore{Root: filepath.Join(root, "blobs")})
	e.Grace = 100 * time.Millisecond
	return e, root
}

func TestFailureClassFixtures(t *testing.T) {
	e, root := newExecutor(t)
	cases := []struct {
		name, command string
		wantReason    string
	}{
		{"nonzero", `printf out; printf err >&2; exit 23`, "nonzero"},
		{"signal", `kill -TERM $$`, "signal"},
		{"timeout", `sleep 30`, "timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inv, err := e.Execute(context.Background(), executor.Request{Command: tc.command, CWD: root, Timeout: 100 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			if inv.Reason == nil || *inv.Reason != tc.wantReason {
				t.Fatalf("reason=%v want %s", inv.Reason, tc.wantReason)
			}
		})
	}
}

func TestDefaultStdinDoesNotBlock(t *testing.T) {
	e, root := newExecutor(t)
	started := time.Now()
	inv, err := e.Execute(context.Background(), executor.Request{Command: `read value; echo "$value"`, CWD: root, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 500*time.Millisecond || inv.State != storage.StateExited {
		t.Fatalf("default stdin blocked: %+v", inv)
	}
}

func TestStreamSeparationAndLargeOutput(t *testing.T) {
	e, root := newExecutor(t)
	inv, err := e.Execute(context.Background(), executor.Request{Command: `seq 20000; printf separate >&2`, CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	stdout := readBlob(t, e.Blobs, inv.Stdout.SHA256)
	stderr := readBlob(t, e.Blobs, inv.Stderr.SHA256)
	if !strings.Contains(stdout, "20000") || stdout == stderr || stderr != "separate" {
		t.Fatalf("streams not separate: %q / %q", stdout[:min(len(stdout), 20)], stderr)
	}
}

func TestDaemonReconciliation(t *testing.T) {
	e, root := newExecutor(t)
	inv, err := e.Execute(context.Background(), executor.Request{Command: `sleep .1`, CWD: root, Background: true})
	if err != nil {
		t.Fatal(err)
	}
	// A missing PID is treated as daemon-lost during restart reconciliation.
	pid := 999999
	if err := e.Store.(*storage.Store).SetPID(context.Background(), inv.ID, pid); err != nil {
		t.Fatal(err)
	}
	count, err := e.Store.(*storage.Store).Reconcile(context.Background(), func(int) bool { return false })
	if err != nil || count != 1 {
		t.Fatalf("reconcile: %v count=%d", err, count)
	}
}

func readBlob(t *testing.T, blobs storage.BlobStore, digest string) string {
	t.Helper()
	f, err := blobs.Open(digest)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
