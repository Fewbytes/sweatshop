package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// serveOnce accepts one connection and, once the request is read, closes
// received and holds the reply until the returned release func is called (the
// test cleanup calls it too, so the server goroutine never outlives the test).
//
// The socket lives directly under TMPDIR rather than t.TempDir(): unix socket
// paths are capped near 104 bytes on macOS, and a temp dir carrying the test
// name overruns it.
func serveOnce(t *testing.T) (socket string, received <-chan struct{}, release func()) {
	t.Helper()
	socket = filepath.Join(os.TempDir(), fmt.Sprintf("agentsh-rpc-%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(socket) })
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	requestRead := make(chan struct{})
	released := make(chan struct{})
	var releaseOnce sync.Once
	release = func() { releaseOnce.Do(func() { close(released) }) }
	t.Cleanup(release)
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
		close(requestRead)
		<-released
		_ = json.NewEncoder(conn).Encode(Success(request.ID, map[string]string{"status": "ok"}))
	}()
	return socket, requestRead, release
}

// A slow command must not be cut off by the connection timeout. The daemon runs
// the command regardless, so a client-side deadline discards work already done.
func TestCallWaitsForSlowResponse(t *testing.T) {
	socket, received, release := serveOnce(t)
	client := Client{Socket: socket, DialTimeout: 10 * time.Millisecond}
	go func() {
		<-received
		release()
	}()

	var value map[string]string
	if err := client.Call(context.Background(), Request{ID: "1", Op: OpBash}, &value); err != nil {
		t.Fatalf("call failed while the response was still coming: %v", err)
	}
	if value["status"] != "ok" {
		t.Fatalf("result = %v", value)
	}
}

func TestCallHonoursContextDeadline(t *testing.T) {
	socket, _, _ := serveOnce(t)
	client := Client{Socket: socket}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := client.Call(ctx, Request{ID: "1", Op: OpBash}, &map[string]string{})
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err = %v, want the context deadline to end the call", err)
	}
}

func TestCallCancellationUnblocksPendingRead(t *testing.T) {
	socket, received, _ := serveOnce(t)
	client := Client{Socket: socket}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-received
		cancel()
	}()
	if err := client.Call(ctx, Request{ID: "1", Op: OpBash}, &map[string]string{}); err == nil {
		t.Fatal("expected cancellation to end the call")
	}
}

func TestTimeoutAppliesOnlyWithoutContextDeadline(t *testing.T) {
	socket, _, _ := serveOnce(t)
	client := Client{Socket: socket, Timeout: 50 * time.Millisecond}
	if err := client.Call(context.Background(), Request{ID: "1", Op: OpBash}, &map[string]string{}); err == nil {
		t.Fatal("expected Timeout to bound the call when ctx carries no deadline")
	}

	socket, received, release := serveOnce(t)
	client = Client{Socket: socket, Timeout: 10 * time.Millisecond}
	go func() {
		<-received
		release()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Call(ctx, Request{ID: "1", Op: OpBash}, &map[string]string{}); err != nil {
		t.Fatalf("context deadline should take precedence over Timeout: %v", err)
	}
}

func TestDialTimeoutDoesNotBoundTheResponse(t *testing.T) {
	socket, received, release := serveOnce(t)
	client := Client{Socket: socket, DialTimeout: 10 * time.Millisecond}
	go func() {
		<-received
		release()
	}()
	if err := client.Call(context.Background(), Request{ID: "1", Op: OpBash}, &map[string]string{}); err != nil {
		t.Fatalf("dial timeout leaked into the response deadline: %v", err)
	}
}
