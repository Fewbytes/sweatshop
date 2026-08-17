package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	agentrpc "github.com/avishai-ish-shalom/sweatshop/agentsh/internal/rpc"
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
