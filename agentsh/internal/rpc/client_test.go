package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// serveOnce accepts one connection, waits delay, then replies.
//
// The socket lives directly under TMPDIR rather than t.TempDir(): unix socket
// paths are capped near 104 bytes on macOS, and a temp dir carrying the test
// name overruns it.
func serveOnce(t *testing.T, delay time.Duration) string {
	t.Helper()
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("agentsh-rpc-%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(socket) })
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request Request
		if err := json.NewDecoder(conn).Decode(&request); err != nil {
			return
		}
		time.Sleep(delay)
		_ = json.NewEncoder(conn).Encode(Success(request.ID, map[string]string{"status": "ok"}))
	}()
	return socket
}

// A slow command must not be cut off by the connection timeout. The daemon runs
// the command regardless, so a client-side deadline discards work already done.
func TestCallWaitsForSlowResponse(t *testing.T) {
	socket := serveOnce(t, 300*time.Millisecond)
	client := Client{Socket: socket, DialTimeout: 50 * time.Millisecond}

	var value map[string]string
	if err := client.Call(context.Background(), Request{ID: "1", Op: OpBash}, &value); err != nil {
		t.Fatalf("call failed while the response was still coming: %v", err)
	}
	if value["status"] != "ok" {
		t.Fatalf("result = %v", value)
	}
}

func TestCallHonoursContextDeadline(t *testing.T) {
	socket := serveOnce(t, 2*time.Second)
	client := Client{Socket: socket}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := client.Call(ctx, Request{ID: "1", Op: OpBash}, &map[string]string{}); err == nil {
		t.Fatal("expected the context deadline to end the call")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("call took %v; context deadline was not applied", elapsed)
	}
}

func TestCallCancellationUnblocksPendingRead(t *testing.T) {
	socket := serveOnce(t, 2*time.Second)
	client := Client{Socket: socket}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if err := client.Call(ctx, Request{ID: "1", Op: OpBash}, &map[string]string{}); err == nil {
		t.Fatal("expected cancellation to end the call")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("call took %v; cancellation did not unblock the read", elapsed)
	}
}

func TestTimeoutAppliesOnlyWithoutContextDeadline(t *testing.T) {
	socket := serveOnce(t, 300*time.Millisecond)
	client := Client{Socket: socket, Timeout: 50 * time.Millisecond}
	if err := client.Call(context.Background(), Request{ID: "1", Op: OpBash}, &map[string]string{}); err == nil {
		t.Fatal("expected Timeout to bound the call when ctx carries no deadline")
	}

	socket = serveOnce(t, 300*time.Millisecond)
	client = Client{Socket: socket, Timeout: 50 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Call(ctx, Request{ID: "1", Op: OpBash}, &map[string]string{}); err != nil {
		t.Fatalf("context deadline should take precedence over Timeout: %v", err)
	}
}

func TestDialTimeoutDoesNotBoundTheResponse(t *testing.T) {
	socket := serveOnce(t, 400*time.Millisecond)
	client := Client{Socket: socket, DialTimeout: 20 * time.Millisecond}
	if err := client.Call(context.Background(), Request{ID: "1", Op: OpBash}, &map[string]string{}); err != nil {
		t.Fatalf("dial timeout leaked into the response deadline: %v", err)
	}
}
