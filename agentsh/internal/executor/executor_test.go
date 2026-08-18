//go:build unix

package executor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestControlPipeOverflowIsCappedAndReported(t *testing.T) {
	exec, _, root := testExecutor(t)
	// Write well past maxControlBytes to fd 3, the pipe used to capture
	// post-command shell state. Without a cap this would grow daemon heap
	// without limit; instead the copy goroutine should stop at the cap and
	// flag the run as truncated rather than hang or OOM.
	inv, err := exec.Execute(context.Background(), Request{
		Command: `head -c $((6*1024*1024)) /dev/zero >&3`,
		CWD:     root,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Stdout.Preview == "" || !strings.Contains(inv.Stdout.Preview, "shell state capture exceeded") {
		t.Fatalf("expected truncation warning in preview, got: %q", inv.Stdout.Preview)
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

func TestExecuteDerivesStructuredSummary(t *testing.T) {
	exec, store, root := testExecutor(t)

	// 1. Supported command (mocking go test output)
	inv, err := exec.Execute(context.Background(), Request{
		Command: `printf '=== RUN   TestSample\n--- PASS: TestSample (0.00s)\nPASS\nok  pkg 0.01s\n'`,
		CWD:     root,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Note: the command was "printf ...", which is not a supported family, so Summary should be nil
	if inv.Summary != nil {
		t.Fatalf("unexpected summary for printf command: %+v", inv.Summary)
	}

	// 2. Command detected as go test via mock binary/script or go test invocation
	// Running "go test" with custom output
	goTestInv, err := exec.Execute(context.Background(), Request{
		Command: `go test -mock-flag 2>/dev/null || printf '=== RUN   TestOne\n--- PASS: TestOne (0.00s)\n=== RUN   TestTwo\n    t_test.go:12: fail\n--- FAIL: TestTwo (0.01s)\nFAIL\n'`,
		CWD:     root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if goTestInv.Summary == nil {
		t.Fatal("expected summary for go test command, got nil")
	}
	if goTestInv.Summary.Family != "go test" {
		t.Errorf("summary family = %q, want 'go test'", goTestInv.Summary.Family)
	}
	if goTestInv.Summary.Passed != 1 || goTestInv.Summary.Failed != 1 {
		t.Errorf("summary counts: passed=%d failed=%d", goTestInv.Summary.Passed, goTestInv.Summary.Failed)
	}

	// Verify persistence in SQLite
	persisted, err := store.GetInvocation(context.Background(), goTestInv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Summary == nil || persisted.Summary.Family != "go test" {
		t.Fatalf("persisted summary = %+v", persisted.Summary)
	}
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
