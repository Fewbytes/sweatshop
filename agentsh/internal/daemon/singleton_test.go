package daemon

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	agentrpc "github.com/avishai-ish-shalom/sweatshop/agentsh/internal/rpc"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/workspace"
)

// startServing launches a daemon and waits until it answers health.
func startServing(t *testing.T, paths workspace.Paths) *Server {
	t.Helper()
	server := New(paths)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = server.Serve(context.Background())
	}()
	t.Cleanup(func() {
		server.Shutdown()
		wg.Wait()
	})

	client := agentrpc.Client{Socket: paths.Socket, Timeout: time.Second}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var health agentrpc.Health
		err := client.Call(context.Background(), agentrpc.Request{ID: "wait", Op: agentrpc.OpHealth}, &health)
		if err == nil {
			return server
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon never began serving: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A second daemon for the same workspace must lose cleanly on the socket, not
// die on a locked database. Opening storage before claiming the socket made the
// "daemon already running" guard unreachable.
func TestSecondDaemonReportsConflictNotDatabaseLock(t *testing.T) {
	paths, err := workspace.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	startServing(t, paths)

	err = New(paths).Serve(context.Background())
	if err == nil {
		t.Fatal("second daemon started alongside the first")
	}
	if strings.Contains(strings.ToLower(err.Error()), "database is locked") {
		t.Errorf("second daemon failed on the database instead of the socket guard: %v", err)
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("expected an 'already running' conflict, got: %v", err)
	}
}

// The losing daemon must not take the winner's socket down with it.
func TestLosingDaemonLeavesWinnerServing(t *testing.T) {
	paths, err := workspace.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	startServing(t, paths)

	if err := New(paths).Serve(context.Background()); err == nil {
		t.Fatal("second daemon started alongside the first")
	}

	client := agentrpc.Client{Socket: paths.Socket, Timeout: time.Second}
	var health agentrpc.Health
	if err := client.Call(context.Background(), agentrpc.Request{ID: "after", Op: agentrpc.OpHealth}, &health); err != nil {
		t.Fatalf("winner no longer reachable after a losing start: %v", err)
	}
	if health.Status != "ok" {
		t.Fatalf("winner unhealthy after a losing start: %+v", health)
	}
}
