//go:build unix

package executor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/storage"
)

func testExecutor(t *testing.T) (*Executor, *storage.Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := storage.Open(context.Background(), storage.Config{Path: filepath.Join(root, "history.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	exec := New(store, storage.BlobStore{Root: filepath.Join(root, "blobs")})
	exec.Grace = 100 * time.Millisecond
	return exec, store, root
}

func TestExecuteSeparatesStreamsAndAppliesEnvironment(t *testing.T) {
	exec, store, root := testExecutor(t)
	inv, err := exec.Execute(context.Background(), Request{Command: `printf '%s' "$TERM,$CI,$PAGER"; printf err >&2; exit 7`, CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	if inv.ExitCode == nil || *inv.ExitCode != 7 || inv.Reason == nil || *inv.Reason != "nonzero" {
		t.Fatalf("unexpected exit: %+v", inv)
	}
	persisted, err := store.GetInvocation(context.Background(), inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readBlob(t, exec.Blobs, persisted.Stdout.SHA256) != "dumb,1,cat" {
		t.Fatal("stdout or environment mismatch")
	}
	if readBlob(t, exec.Blobs, persisted.Stderr.SHA256) != "err" {
		t.Fatal("stderr mismatch")
	}
}

func TestTimeoutKillsProcessGroup(t *testing.T) {
	exec, _, root := testExecutor(t)
	pidFile := filepath.Join(root, "child.pid")
	inv, err := exec.Execute(context.Background(), Request{Command: `sleep 30 & echo $! > child.pid; wait`, CWD: root, Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if inv.State != storage.StateTimeout || inv.Reason == nil || *inv.Reason != "timeout" {
		t.Fatalf("unexpected timeout: %+v", inv)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		err = syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child %d survived timeout", pid)
}

func TestBackgroundReturnsBeforeCompletion(t *testing.T) {
	exec, store, root := testExecutor(t)
	started := time.Now()
	inv, err := exec.Execute(context.Background(), Request{Command: `sleep .2; echo done`, CWD: root, Background: true})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 150*time.Millisecond || inv.State != storage.StateRunning {
		t.Fatalf("background did not return promptly: %+v", inv)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, getErr := store.GetInvocation(context.Background(), inv.ID)
		if getErr == nil && got.State == storage.StateExited {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("background invocation did not complete")
}

func readBlob(t *testing.T, store storage.BlobStore, digest string) string {
	t.Helper()
	file, err := store.Open(digest)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
