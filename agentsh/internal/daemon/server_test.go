package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/output"
	agentrpc "github.com/avishai-ish-shalom/sweatshop/agentsh/internal/rpc"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/storage"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/workspace"
)

func TestHealthAndShutdown(t *testing.T) {
	root := t.TempDir()
	paths, err := workspace.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	server := New(paths)
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()

	client := agentrpc.Client{Socket: paths.Socket, Timeout: time.Second}
	deadline := time.Now().Add(time.Second)
	var health agentrpc.Health
	for {
		err = client.Call(context.Background(), agentrpc.Request{ID: "test", Op: agentrpc.OpHealth}, &health)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if health.Workspace != root || health.Status != "ok" {
		t.Fatalf("unexpected health: %+v", health)
	}
	if err := client.Call(context.Background(), agentrpc.Request{ID: "stop", Op: agentrpc.OpShutdown}, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains after shutdown: %v", err)
	}
}

func TestSecondServerDoesNotStealLiveSocket(t *testing.T) {
	paths, _ := workspace.Resolve(t.TempDir())
	first := New(paths)
	done := make(chan error, 1)
	go func() { done <- first.Serve(context.Background()) }()
	waitForSocket(t, paths.Socket)

	second := New(paths)
	if err := second.Serve(context.Background()); err == nil {
		t.Fatal("second daemon unexpectedly started")
	}
	first.Shutdown()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", path)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("socket did not become ready")
}

func TestBashOutputPagesViaIndex(t *testing.T) {
	root := t.TempDir()
	paths, err := workspace.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	server := New(paths)
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	defer func() {
		server.Shutdown()
		<-done
	}()

	client := agentrpc.Client{Socket: paths.Socket, Timeout: 5 * time.Second}
	waitForSocket(t, paths.Socket)

	params, _ := json.Marshal(agentrpc.BashRequest{Command: "seq 1000", Session: "default"})
	var invocation storage.Invocation
	if err := client.Call(context.Background(), agentrpc.Request{ID: "run", Op: agentrpc.OpBash, Params: params}, &invocation); err != nil {
		t.Fatal(err)
	}
	if invocation.Stdout.SHA256 == "" {
		t.Fatalf("no stdout digest: %+v", invocation)
	}
	if _, err := os.Stat(filepath.Join(paths.Index, invocation.Stdout.SHA256+".idx")); err != nil {
		t.Fatalf("sidecar index not written: %v", err)
	}

	outParams, _ := json.Marshal(agentrpc.BashOutputRequest{ID: invocation.ID, Stream: "stdout", Lines: "500:503"})
	var result output.Result
	if err := client.Call(context.Background(), agentrpc.Request{ID: "out", Op: agentrpc.OpBashOutput, Params: outParams}, &result); err != nil {
		t.Fatal(err)
	}
	if result.Text != "500:500\n501:501\n502:502\n503:503" {
		t.Fatalf("paged output = %q", result.Text)
	}
	if result.Lines != 1000 || result.Bytes != int64(invocation.Stdout.Bytes) {
		t.Fatalf("paged metadata = %+v", result)
	}
}

func TestBashOutputGrep(t *testing.T) {
	root := t.TempDir()
	paths, err := workspace.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	server := New(paths)
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	defer func() {
		server.Shutdown()
		<-done
	}()

	client := agentrpc.Client{Socket: paths.Socket, Timeout: 5 * time.Second}
	waitForSocket(t, paths.Socket)

	params, _ := json.Marshal(agentrpc.BashRequest{Command: `printf 'alpha\nerror one\nbeta\nerror two\n'`, Session: "default"})
	var invocation storage.Invocation
	if err := client.Call(context.Background(), agentrpc.Request{ID: "run", Op: agentrpc.OpBash, Params: params}, &invocation); err != nil {
		t.Fatal(err)
	}

	outParams, _ := json.Marshal(agentrpc.BashOutputRequest{ID: invocation.ID, Stream: "stdout", Grep: "error"})
	var result output.Result
	if err := client.Call(context.Background(), agentrpc.Request{ID: "grep", Op: agentrpc.OpBashOutput, Params: outParams}, &result); err != nil {
		t.Fatal(err)
	}
	if result.Matches != 2 {
		t.Fatalf("grep matches = %d, want 2 (%+v)", result.Matches, result)
	}
	if !strings.Contains(result.Text, "error one") || !strings.Contains(result.Text, "error two") {
		t.Fatalf("grep text = %q", result.Text)
	}
}

func TestBashReturnsStructuredSummary(t *testing.T) {
	root := t.TempDir()
	paths, err := workspace.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	server := New(paths)
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	defer func() {
		server.Shutdown()
		<-done
	}()

	client := agentrpc.Client{Socket: paths.Socket, Timeout: 5 * time.Second}
	waitForSocket(t, paths.Socket)

	// Command detected as pytest with failure output
	pytestCmd := `python3 -m pytest tests/ 2>/dev/null || printf '============================= test session starts ==============================\ncollected 2 items\n\ntests/test_app.py .F                                                     [100%%]\n\n=================================== FAILURES ===================================\n__________________________________ test_fail ___________________________________\ntests/test_app.py:10: AssertionError\n=========================== short test summary info ============================\nFAILED tests/test_app.py::test_fail - AssertionError: assert False\n========================= 1 failed, 1 passed in 0.05s ==========================\n'`
	params, _ := json.Marshal(agentrpc.BashRequest{Command: pytestCmd, Session: "default"})
	var invocation storage.Invocation
	if err := client.Call(context.Background(), agentrpc.Request{ID: "run-pytest", Op: agentrpc.OpBash, Params: params}, &invocation); err != nil {
		t.Fatal(err)
	}
	if invocation.Summary == nil {
		t.Fatal("expected non-nil summary on pytest command")
	}
	if invocation.Summary.Family != "pytest" || invocation.Summary.Passed != 1 || invocation.Summary.Failed != 1 {
		t.Fatalf("unexpected summary: %+v", invocation.Summary)
	}
	if len(invocation.Summary.Failures) == 0 {
		t.Fatal("expected failure frame in summary")
	}

	// Unknown command returns nil summary
	unknownParams, _ := json.Marshal(agentrpc.BashRequest{Command: "echo 'plain command'", Session: "default"})
	var unknownInv storage.Invocation
	if err := client.Call(context.Background(), agentrpc.Request{ID: "run-unknown", Op: agentrpc.OpBash, Params: unknownParams}, &unknownInv); err != nil {
		t.Fatal(err)
	}
	if unknownInv.Summary != nil {
		t.Fatalf("expected nil summary for unknown command, got %+v", unknownInv.Summary)
	}
}
