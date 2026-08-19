package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"syscall"
	"testing"

	agentrpc "github.com/Fewbytes/sweatshop/agentsh/internal/rpc"
)

// everyOpHasACommand is the acceptance criterion made executable: every RPC
// op the protocol package defines must have a CLI command that produces it.
func TestEveryRPCOpHasACLICommand(t *testing.T) {
	ops := []string{
		agentrpc.OpHealth, agentrpc.OpShutdown, agentrpc.OpBash, agentrpc.OpBashOutput,
		agentrpc.OpBashInput, agentrpc.OpBashKill, agentrpc.OpBashState, agentrpc.OpBashHistory,
		agentrpc.OpBashReplay, agentrpc.OpBashProcesses, agentrpc.OpBashGC, agentrpc.OpBashTemplates,
		agentrpc.OpBashService, agentrpc.OpBashServiceStatus, agentrpc.OpBashServiceKill, agentrpc.OpBashServiceLogs,
	}
	covered := make(map[string]bool)
	for name, cmd := range commands {
		args := minimalArgsFor(name)
		req, err := cmd.build(args)
		if err != nil {
			t.Fatalf("command %q: build(%v) failed: %v", name, args, err)
		}
		covered[req.Op] = true
	}
	for _, op := range ops {
		if !covered[op] {
			t.Errorf("RPC op %q has no CLI command producing it", op)
		}
	}
}

// minimalArgsFor supplies the smallest valid positional-argument set for a
// command, so build() succeeds without exercising every flag.
func minimalArgsFor(name string) []string {
	switch name {
	case "bash":
		return []string{"echo hi"}
	case "output", "kill", "replay", "templates", "service-status", "service-kill", "service-logs":
		return []string{"inv_x"}
	case "input":
		return []string{"inv_x", "data"}
	case "service":
		return []string{"web", "sleep 30"}
	default:
		return nil
	}
}

func TestBuildBashSetsAllFields(t *testing.T) {
	req, err := buildBash([]string{
		"--session", "s1", "--timeout", "5s", "--idle-wait", "2s",
		"--background", "--interactive", "--stdin", "hello", "echo hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Op != agentrpc.OpBash {
		t.Fatalf("Op = %q", req.Op)
	}
	var params agentrpc.BashRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatal(err)
	}
	want := agentrpc.BashRequest{
		Command: "echo hi", Session: "s1", TimeoutMS: 5000, IdleWaitMS: 2000,
		Background: true, Interactive: true, Stdin: "hello",
	}
	if params != want {
		t.Fatalf("params = %+v, want %+v", params, want)
	}
}

func TestBuildBashRejectsWrongArgCount(t *testing.T) {
	if _, err := buildBash(nil); err == nil {
		t.Fatal("expected error with no COMMAND argument")
	}
	if _, err := buildBash([]string{"one", "two"}); err == nil {
		t.Fatal("expected error with two positional arguments")
	}
}

func TestBuildOutputSetsAllFields(t *testing.T) {
	req, err := buildOutput([]string{
		"--stream", "stderr", "--lines", "1:10", "--grep", "err", "--context", "2", "--mode", "templates", "inv_x",
	})
	if err != nil {
		t.Fatal(err)
	}
	var params agentrpc.BashOutputRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatal(err)
	}
	want := agentrpc.BashOutputRequest{ID: "inv_x", Stream: "stderr", Lines: "1:10", Grep: "err", Context: 2, Mode: "templates"}
	if params != want {
		t.Fatalf("params = %+v, want %+v", params, want)
	}
}

func TestBuildHistoryExitFilterUnsetByDefault(t *testing.T) {
	req, err := buildHistory(nil)
	if err != nil {
		t.Fatal(err)
	}
	var params agentrpc.BashHistoryRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Exit != nil {
		t.Fatalf("Exit = %v, want nil (unset) when -exit is not passed", params.Exit)
	}
}

func TestBuildHistoryExitFilterSet(t *testing.T) {
	req, err := buildHistory([]string{"--exit", "0"})
	if err != nil {
		t.Fatal(err)
	}
	var params agentrpc.BashHistoryRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Exit == nil || *params.Exit != 0 {
		t.Fatalf("Exit = %v, want *0", params.Exit)
	}
}

func TestBuildServiceReadinessOmittedByDefault(t *testing.T) {
	req, err := buildService([]string{"web", "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	var params agentrpc.BashServiceRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Readiness != nil {
		t.Fatalf("Readiness = %+v, want nil when no -ready-* flag is passed", params.Readiness)
	}
}

func TestBuildServiceReadinessSetWhenAnyReadyFlagPassed(t *testing.T) {
	req, err := buildService([]string{"--ready-port", "8080", "web", "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	var params agentrpc.BashServiceRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Readiness == nil || params.Readiness.Port != 8080 {
		t.Fatalf("Readiness = %+v, want Port=8080", params.Readiness)
	}
}

func TestUnavailableMatchesOnlyConnectionRefusedOrMissingSocket(t *testing.T) {
	if !unavailable(&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}) {
		t.Error("expected ECONNREFUSED to be treated as unavailable")
	}
	if !unavailable(&net.OpError{Op: "dial", Err: &os.PathError{Op: "connect", Err: syscall.ENOENT}}) {
		t.Error("expected ENOENT to be treated as unavailable")
	}
	if unavailable(&net.OpError{Op: "dial", Err: syscall.EACCES}) {
		t.Error("permission error must not be treated as unavailable (would spuriously spawn a daemon)")
	}
	if unavailable(errors.New("some other failure")) {
		t.Error("an unrelated error must not be treated as unavailable")
	}
}
