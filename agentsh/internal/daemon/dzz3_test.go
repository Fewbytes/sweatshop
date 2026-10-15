package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/agentsh/internal/output"
	agentrpc "github.com/Fewbytes/sweatshop/agentsh/internal/rpc"
	"github.com/Fewbytes/sweatshop/agentsh/internal/testwait"
	"github.com/Fewbytes/sweatshop/agentsh/internal/workspace"
)

func startTestServer(t *testing.T) (*Server, agentrpc.Client) {
	t.Helper()
	root := t.TempDir()
	paths, err := workspace.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	server := New(paths)
	server.Grace = 100 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	t.Cleanup(func() {
		server.Shutdown()
		<-done
	})
	waitForSocket(t, paths.Socket)
	return server, agentrpc.Client{Socket: paths.Socket, Timeout: 5 * time.Second}
}

func TestBashServiceLogsUnknownNameReturnsClearError(t *testing.T) {
	_, client := startTestServer(t)
	params, _ := json.Marshal(agentrpc.BashServiceLogsRequest{Name: "ghost"})
	err := client.Call(context.Background(), agentrpc.Request{ID: "logs", Op: agentrpc.OpBashServiceLogs, Params: params}, nil)
	if err == nil {
		t.Fatal("expected an error for an unknown service name")
	}
}

func TestBashServiceLogsPagesByLineRange(t *testing.T) {
	server, client := startTestServer(t)
	startParams, _ := json.Marshal(agentrpc.BashServiceRequest{Name: "counter", Command: `for i in $(seq 1 20); do echo "line $i"; done; sleep 30`, Session: "default"})
	if err := client.Call(context.Background(), agentrpc.Request{ID: "start", Op: agentrpc.OpBashService, Params: startParams}, nil); err != nil {
		t.Fatal(err)
	}
	fullLogParams, _ := json.Marshal(agentrpc.BashServiceLogsRequest{Name: "counter", Lines: "1:20"})
	testwait.Until(t, 2*time.Second, "service log to reach line 20", func() bool {
		var result output.Result
		err := client.Call(context.Background(), agentrpc.Request{ID: "logs", Op: agentrpc.OpBashServiceLogs, Params: fullLogParams}, &result)
		return err == nil && strings.Contains(result.Text, "line 20")
	})

	logsParams, _ := json.Marshal(agentrpc.BashServiceLogsRequest{Name: "counter", Lines: "5:8"})
	var result output.Result
	if err := client.Call(context.Background(), agentrpc.Request{ID: "logs2", Op: agentrpc.OpBashServiceLogs, Params: logsParams}, &result); err != nil {
		t.Fatal(err)
	}
	if result.Text != "5:line 5\n6:line 6\n7:line 7\n8:line 8" {
		t.Fatalf("unexpected paged text: %q", result.Text)
	}

	killParams, _ := json.Marshal(agentrpc.BashServiceKillRequest{Name: "counter"})
	if err := client.Call(context.Background(), agentrpc.Request{ID: "kill", Op: agentrpc.OpBashServiceKill, Params: killParams}, nil); err != nil {
		t.Fatal(err)
	}
	waitForNoRunningInvocations(t, server)
}

func TestBashServiceLogsTailReturnsLastNLines(t *testing.T) {
	server, client := startTestServer(t)
	startParams, _ := json.Marshal(agentrpc.BashServiceRequest{Name: "counter", Command: `for i in $(seq 1 20); do echo "line $i"; done; sleep 30`, Session: "default"})
	if err := client.Call(context.Background(), agentrpc.Request{ID: "start", Op: agentrpc.OpBashService, Params: startParams}, nil); err != nil {
		t.Fatal(err)
	}

	var result output.Result
	logsParams, _ := json.Marshal(agentrpc.BashServiceLogsRequest{Name: "counter", Tail: 3})
	testwait.Until(t, 2*time.Second, "service log to reach line 20", func() bool {
		err := client.Call(context.Background(), agentrpc.Request{ID: "logs", Op: agentrpc.OpBashServiceLogs, Params: logsParams}, &result)
		return err == nil && strings.Contains(result.Text, "line 20")
	})
	if result.Text != "line 18\nline 19\nline 20" {
		t.Fatalf("unexpected tail text: %q", result.Text)
	}

	killParams, _ := json.Marshal(agentrpc.BashServiceKillRequest{Name: "counter"})
	if err := client.Call(context.Background(), agentrpc.Request{ID: "kill", Op: agentrpc.OpBashServiceKill, Params: killParams}, nil); err != nil {
		t.Fatal(err)
	}
	waitForNoRunningInvocations(t, server)
}

func TestBashServiceLogsNonRunningReturnsLastOutput(t *testing.T) {
	server, client := startTestServer(t)
	startParams, _ := json.Marshal(agentrpc.BashServiceRequest{Name: "onceoff", Command: `echo done; exit 0`, Session: "default"})
	if err := client.Call(context.Background(), agentrpc.Request{ID: "start", Op: agentrpc.OpBashService, Params: startParams}, nil); err != nil {
		t.Fatal(err)
	}
	waitForNoRunningInvocations(t, server)

	logsParams, _ := json.Marshal(agentrpc.BashServiceLogsRequest{Name: "onceoff"})
	var result output.Result
	if err := client.Call(context.Background(), agentrpc.Request{ID: "logs", Op: agentrpc.OpBashServiceLogs, Params: logsParams}, &result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "done") {
		t.Fatalf("expected last output from a crashed/stopped service, got: %q", result.Text)
	}
}

func TestBashServiceLogsFollowCollectsNewOutputUntilIdle(t *testing.T) {
	server, client := startTestServer(t)
	startParams, _ := json.Marshal(agentrpc.BashServiceRequest{
		Name: "streamer", Session: "default",
		Command: `echo one; sleep 0.15; echo two; sleep 0.15; echo three; sleep 30`,
	})
	if err := client.Call(context.Background(), agentrpc.Request{ID: "start", Op: agentrpc.OpBashService, Params: startParams}, nil); err != nil {
		t.Fatal(err)
	}

	logsParams, _ := json.Marshal(agentrpc.BashServiceLogsRequest{
		Name: "streamer", Follow: true, FollowIdleMS: 400, FollowTimeoutMS: 3000,
	})
	var result output.Result
	if err := client.Call(context.Background(), agentrpc.Request{ID: "logs", Op: agentrpc.OpBashServiceLogs, Params: logsParams}, &result); err != nil {
		t.Fatal(err)
	}
	if result.Text != "one\ntwo\nthree" {
		t.Fatalf("unexpected follow text: %q", result.Text)
	}
	if !result.Running {
		t.Fatal("expected the service to still be reported running")
	}

	killParams, _ := json.Marshal(agentrpc.BashServiceKillRequest{Name: "streamer"})
	if err := client.Call(context.Background(), agentrpc.Request{ID: "kill", Op: agentrpc.OpBashServiceKill, Params: killParams}, nil); err != nil {
		t.Fatal(err)
	}
	waitForNoRunningInvocations(t, server)
}

func TestBashServiceLogsFollowStopsWhenMaxLinesReached(t *testing.T) {
	server, client := startTestServer(t)
	startParams, _ := json.Marshal(agentrpc.BashServiceRequest{
		Name: "spammer", Session: "default",
		Command: `for i in $(seq 1 50); do echo "line $i"; sleep 0.01; done; sleep 30`,
	})
	if err := client.Call(context.Background(), agentrpc.Request{ID: "start", Op: agentrpc.OpBashService, Params: startParams}, nil); err != nil {
		t.Fatal(err)
	}

	logsParams, _ := json.Marshal(agentrpc.BashServiceLogsRequest{
		Name: "spammer", Follow: true, FollowIdleMS: 5000, FollowTimeoutMS: 5000, FollowMaxLines: 5,
	})
	start := time.Now()
	var result output.Result
	if err := client.Call(context.Background(), agentrpc.Request{ID: "logs", Op: agentrpc.OpBashServiceLogs, Params: logsParams}, &result); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("follow took %v, should have stopped early at max lines", elapsed)
	}
	if result.Lines != 5 {
		t.Fatalf("Lines = %d, want 5 (FollowMaxLines cap)", result.Lines)
	}

	killParams, _ := json.Marshal(agentrpc.BashServiceKillRequest{Name: "spammer"})
	if err := client.Call(context.Background(), agentrpc.Request{ID: "kill", Op: agentrpc.OpBashServiceKill, Params: killParams}, nil); err != nil {
		t.Fatal(err)
	}
	waitForNoRunningInvocations(t, server)
}
