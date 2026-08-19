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

	"github.com/Fewbytes/sweatshop/agentsh/internal/output"
	agentrpc "github.com/Fewbytes/sweatshop/agentsh/internal/rpc"
	"github.com/Fewbytes/sweatshop/agentsh/internal/storage"
	"github.com/Fewbytes/sweatshop/agentsh/internal/workspace"
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

func TestBashTemplatesAndBaselineComparison(t *testing.T) {
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

	// Run 1: Standard repetitive logs
	scriptFile := filepath.Join(root, "worker.sh")
	if err := os.WriteFile(scriptFile, []byte("#!/bin/bash\nprintf '2026-08-17 10:00:01 INFO Connection from 192.168.1.1:1000\\n2026-08-17 10:00:02 INFO Connection from 192.168.1.2:2000\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	p1, _ := json.Marshal(agentrpc.BashRequest{Command: "bash worker.sh", Session: "default"})
	var inv1 storage.Invocation
	if err := client.Call(context.Background(), agentrpc.Request{ID: "r1", Op: agentrpc.OpBash, Params: p1}, &inv1); err != nil {
		t.Fatal(err)
	}

	// Query templates for Run 1 via OpBashTemplates
	tReq1, _ := json.Marshal(agentrpc.BashTemplatesRequest{ID: inv1.ID, Stream: "stdout", Baseline: false})
	var analysis1 struct {
		TotalLines int `json:"total_lines"`
		Templates  []struct {
			Template string `json:"template"`
			Count    int    `json:"count"`
		} `json:"templates"`
		Levels map[string]int `json:"levels"`
	}
	if err := client.Call(context.Background(), agentrpc.Request{ID: "t1", Op: agentrpc.OpBashTemplates, Params: tReq1}, &analysis1); err != nil {
		t.Fatal(err)
	}
	if analysis1.TotalLines != 2 || len(analysis1.Templates) != 1 {
		t.Fatalf("unexpected analysis1: %+v", analysis1)
	}
	if analysis1.Templates[0].Count != 2 {
		t.Errorf("template count = %d, want 2", analysis1.Templates[0].Count)
	}

	// Run 2: Same command, but introduces a new novel error
	if err := os.WriteFile(scriptFile, []byte("#!/bin/bash\nprintf '2026-08-17 10:00:03 INFO Connection from 192.168.1.3:3000\\n2026-08-17 10:00:04 ERROR Database connection timeout to 10.0.0.5:5432\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	p2, _ := json.Marshal(agentrpc.BashRequest{Command: "bash worker.sh", Session: "default"})
	var inv2 storage.Invocation
	if err := client.Call(context.Background(), agentrpc.Request{ID: "r2", Op: agentrpc.OpBash, Params: p2}, &inv2); err != nil {
		t.Fatal(err)
	}

	// Query templates for Run 2 with Baseline comparison enabled
	tReq2, _ := json.Marshal(agentrpc.BashTemplatesRequest{ID: inv2.ID, Stream: "stdout", Baseline: true})
	var analysis2 struct {
		TotalLines int `json:"total_lines"`
		Templates  []struct {
			ID       string `json:"id"`
			Template string `json:"template"`
			Count    int    `json:"count"`
			Novel    bool   `json:"novel"`
			Level    string `json:"level"`
		} `json:"templates"`
		Summary string `json:"summary"`
	}
	if err := client.Call(context.Background(), agentrpc.Request{ID: "t2", Op: agentrpc.OpBashTemplates, Params: tReq2}, &analysis2); err != nil {
		t.Fatal(err)
	}
	if len(analysis2.Templates) != 2 {
		t.Fatalf("expected 2 templates in run 2, got %d", len(analysis2.Templates))
	}
	// The novel error template should be prioritized at top
	top := analysis2.Templates[0]
	if !top.Novel || top.Level != "ERROR" {
		t.Errorf("top template is not novel error: %+v", top)
	}
	if !strings.Contains(analysis2.Summary, "1 novel template (1 error)") {
		t.Errorf("summary missing novel error: %q", analysis2.Summary)
	}

	// Also verify BashOutput mode="templates"
	outReq, _ := json.Marshal(agentrpc.BashOutputRequest{ID: inv2.ID, Stream: "stdout", Mode: "templates"})
	var outRes output.Result
	if err := client.Call(context.Background(), agentrpc.Request{ID: "out-tmpl", Op: agentrpc.OpBashOutput, Params: outReq}, &outRes); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outRes.Text, "Database connection timeout") {
		t.Errorf("BashOutput templates mode output missing error text: %q", outRes.Text)
	}
}
