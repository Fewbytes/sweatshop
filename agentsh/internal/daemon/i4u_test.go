package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentrpc "github.com/avishai-ish-shalom/sweatshop/agentsh/internal/rpc"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/storage"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/workspace"
)

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("file %s never appeared", path)
}

func TestClientDisconnectCancelsForegroundInvocation(t *testing.T) {
	root := t.TempDir()
	paths, err := workspace.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	// Grace is set before Serve starts so the executor never has it read
	// and written from different goroutines.
	server := New(paths)
	server.Grace = 100 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	defer func() {
		server.Shutdown()
		<-done
	}()
	waitForSocket(t, paths.Socket)

	marker := filepath.Join(root, "marker")
	cmd := fmt.Sprintf(`echo started >> %s; sleep 30; echo done >> %s`, marker, marker)
	reqParams, _ := json.Marshal(agentrpc.BashRequest{Command: cmd, Session: "default"})
	req := agentrpc.Request{Version: agentrpc.Version, ID: "disconnect-me", Op: agentrpc.OpBash, Params: reqParams}

	conn, err := net.Dial("unix", paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		t.Fatal(err)
	}
	// Wait for the process to actually start before disconnecting, so this
	// races a live process rather than one that hasn't been spawned yet.
	waitForFile(t, marker)
	conn.Close() // disconnect without reading the response

	// If cancellation works, "done" is never appended (SIGTERM/SIGKILL cuts
	// off the sleep) and a fresh health check keeps succeeding, proving the
	// daemon itself stayed healthy throughout.
	client := agentrpc.Client{Socket: paths.Socket, Timeout: 2 * time.Second}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var health agentrpc.Health
		if err := client.Call(context.Background(), agentrpc.Request{ID: "h", Op: agentrpc.OpHealth}, &health); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(marker)
		if strings.Contains(string(data), "done") {
			t.Fatal("process ran to completion; disconnect did not cancel it")
		}
		time.Sleep(50 * time.Millisecond)
	}
	data, _ := os.ReadFile(marker)
	if strings.Contains(string(data), "done") {
		t.Fatal("process ran to completion; disconnect did not cancel it")
	}
}

func TestShutdownReturnsWithinGraceDespiteLongForegroundCommand(t *testing.T) {
	root := t.TempDir()
	paths, err := workspace.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	server := New(paths)
	server.Grace = 100 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	waitForSocket(t, paths.Socket)

	marker := filepath.Join(root, "marker")
	client := agentrpc.Client{Socket: paths.Socket, Timeout: 10 * time.Second}
	runDone := make(chan error, 1)
	go func() {
		params, _ := json.Marshal(agentrpc.BashRequest{Command: fmt.Sprintf(`echo started >> %s; sleep 30`, marker), Session: "default"})
		var inv storage.Invocation
		runDone <- client.Call(context.Background(), agentrpc.Request{ID: "long", Op: agentrpc.OpBash, Params: params}, &inv)
	}()
	waitForFile(t, marker)

	shutdownStart := time.Now()
	server.Shutdown()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return within timeout after Shutdown with an in-flight command")
	}
	if elapsed := time.Since(shutdownStart); elapsed > 2*time.Second {
		t.Fatalf("shutdown took too long with a long-running foreground command: %v", elapsed)
	}
	<-runDone
}
