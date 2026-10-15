package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	agentrpc "github.com/Fewbytes/sweatshop/agentsh/internal/rpc"
	"github.com/Fewbytes/sweatshop/agentsh/internal/storage"
	"github.com/Fewbytes/sweatshop/agentsh/internal/testwait"
	"github.com/Fewbytes/sweatshop/agentsh/internal/workspace"
)

func waitForFile(t *testing.T, path string) {
	t.Helper()
	testwait.Until(t, 2*time.Second, "file "+path, func() bool {
		_, err := os.Stat(path)
		return err == nil
	})
}

// waitForPIDFile returns the pid a test command wrote with `echo $$ > path`,
// tolerating the window where the file exists but is still empty.
func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	var pid int
	testwait.Until(t, 2*time.Second, "pid file "+path, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(data)))
		pid = n
		return err == nil
	})
	return pid
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
	pidFile := filepath.Join(root, "pid")
	cmd := fmt.Sprintf(`echo $$ > %s; sleep 30; echo done >> %s`, pidFile, marker)
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
	pid := waitForPIDFile(t, pidFile)
	conn.Close() // disconnect without reading the response

	// If cancellation works, the shell dies (SIGTERM/SIGKILL cuts off the
	// sleep) before it can append "done", and the daemon still answers health.
	testwait.Until(t, 3*time.Second, "cancelled command to exit", func() bool { return !processAlive(pid) })
	client := agentrpc.Client{Socket: paths.Socket, Timeout: 2 * time.Second}
	var health agentrpc.Health
	if err := client.Call(context.Background(), agentrpc.Request{ID: "h", Op: agentrpc.OpHealth}, &health); err != nil {
		t.Fatal(err)
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
