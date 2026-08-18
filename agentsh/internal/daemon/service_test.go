package daemon

import (
	"context"
	"encoding/json"
	"syscall"
	"testing"
	"time"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/executor"
	agentrpc "github.com/avishai-ish-shalom/sweatshop/agentsh/internal/rpc"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/workspace"
)

func TestBashServiceLifecycleOverRPC(t *testing.T) {
	root := t.TempDir()
	paths, err := workspace.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	server := New(paths)
	server.Grace = 100 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	defer func() {
		server.Shutdown()
		<-done
	}()
	waitForSocket(t, paths.Socket)

	client := agentrpc.Client{Socket: paths.Socket, Timeout: 5 * time.Second}

	startParams, _ := json.Marshal(agentrpc.BashServiceRequest{Name: "web", Command: "sleep 30", Session: "default"})
	var svc executor.Service
	if err := client.Call(context.Background(), agentrpc.Request{ID: "start", Op: agentrpc.OpBashService, Params: startParams}, &svc); err != nil {
		t.Fatal(err)
	}
	if svc.InvocationID == "" || svc.State != executor.ServiceStateRunning {
		t.Fatalf("unexpected start result: %+v", svc)
	}

	statusParams, _ := json.Marshal(agentrpc.BashServiceStatusRequest{Name: "web"})
	var status executor.Service
	if err := client.Call(context.Background(), agentrpc.Request{ID: "status", Op: agentrpc.OpBashServiceStatus, Params: statusParams}, &status); err != nil {
		t.Fatal(err)
	}
	if status.State != executor.ServiceStateRunning || status.PID != svc.PID {
		t.Fatalf("unexpected status result: %+v", status)
	}

	// Starting a second service under the same name while the first is
	// still running must fail.
	if err := client.Call(context.Background(), agentrpc.Request{ID: "dup", Op: agentrpc.OpBashService, Params: startParams}, &svc); err == nil {
		t.Fatal("expected duplicate service start to fail")
	}

	killParams, _ := json.Marshal(agentrpc.BashServiceKillRequest{Name: "web"})
	if err := client.Call(context.Background(), agentrpc.Request{ID: "kill", Op: agentrpc.OpBashServiceKill, Params: killParams}, nil); err != nil {
		t.Fatal(err)
	}

	// The name is freed once killed, so status on it now reads as unknown.
	if err := client.Call(context.Background(), agentrpc.Request{ID: "status2", Op: agentrpc.OpBashServiceStatus, Params: statusParams}, &status); err == nil {
		t.Fatal("expected status on a killed-and-removed service to error")
	}

	// And the name is free to reuse.
	var restarted executor.Service
	if err := client.Call(context.Background(), agentrpc.Request{ID: "restart", Op: agentrpc.OpBashService, Params: startParams}, &restarted); err != nil {
		t.Fatal(err)
	}
	killParams2, _ := json.Marshal(agentrpc.BashServiceKillRequest{Name: "web"})
	if err := client.Call(context.Background(), agentrpc.Request{ID: "kill2", Op: agentrpc.OpBashServiceKill, Params: killParams2}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestServicesSurviveDaemonRestartAsStopped(t *testing.T) {
	root := t.TempDir()
	paths, err := workspace.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}

	first := New(paths)
	done := make(chan error, 1)
	go func() { done <- first.Serve(context.Background()) }()
	waitForSocket(t, paths.Socket)

	client := agentrpc.Client{Socket: paths.Socket, Timeout: 5 * time.Second}
	startParams, _ := json.Marshal(agentrpc.BashServiceRequest{Name: "web", Command: "sleep 30", Session: "default"})
	var svc executor.Service
	if err := client.Call(context.Background(), agentrpc.Request{ID: "start", Op: agentrpc.OpBashService, Params: startParams}, &svc); err != nil {
		t.Fatal(err)
	}

	// Simulate a crash: the service process dies out from under the daemon
	// (not via BashServiceKill, which would clean up the record itself),
	// and the daemon restarts without ever seeing it happen.
	if svc.PID == 0 {
		t.Fatalf("expected a PID for the started service: %+v", svc)
	}
	if err := syscall.Kill(svc.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && processAlive(svc.PID) {
		time.Sleep(10 * time.Millisecond)
	}
	if processAlive(svc.PID) {
		t.Fatal("service process did not die")
	}

	first.Shutdown()
	<-done

	second := New(paths)
	done2 := make(chan error, 1)
	go func() { done2 <- second.Serve(context.Background()) }()
	defer func() {
		second.Shutdown()
		<-done2
	}()
	waitForSocket(t, paths.Socket)

	statusParams, _ := json.Marshal(agentrpc.BashServiceStatusRequest{Name: "web"})
	var status executor.Service
	if err := client.Call(context.Background(), agentrpc.Request{ID: "status", Op: agentrpc.OpBashServiceStatus, Params: statusParams}, &status); err != nil {
		t.Fatal(err)
	}
	if status.State != executor.ServiceStateStopped {
		t.Fatalf("state = %q, want stopped (record survives restart, process gone)", status.State)
	}
	if status.InvocationID != svc.InvocationID {
		t.Fatalf("invocation id changed across restart: %q -> %q", svc.InvocationID, status.InvocationID)
	}
}

func TestBashServiceUnknownNameReturnsClearError(t *testing.T) {
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
	waitForSocket(t, paths.Socket)

	client := agentrpc.Client{Socket: paths.Socket, Timeout: 5 * time.Second}
	statusParams, _ := json.Marshal(agentrpc.BashServiceStatusRequest{Name: "ghost"})
	err = client.Call(context.Background(), agentrpc.Request{ID: "status", Op: agentrpc.OpBashServiceStatus, Params: statusParams}, nil)
	if err == nil {
		t.Fatal("expected an error for an unknown service name")
	}
}
