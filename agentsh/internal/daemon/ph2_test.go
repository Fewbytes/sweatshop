package daemon

import (
	"context"
	"encoding/json"
	"testing"

	agentrpc "github.com/Fewbytes/sweatshop/agentsh/internal/rpc"
	"github.com/Fewbytes/sweatshop/agentsh/internal/storage"
)

// TestDispatchTableCoversEveryDeclaredOp makes "handle() body is a table
// lookup" (sweatshop-ph2's first acceptance bullet) an executable check:
// every rpc.Op* constant except the version-mismatch/decode-error paths
// handled before dispatch must have a handler in the table.
func TestDispatchTableCoversEveryDeclaredOp(t *testing.T) {
	ops := []string{
		agentrpc.OpHealth, agentrpc.OpShutdown, agentrpc.OpBash, agentrpc.OpBashOutput,
		agentrpc.OpBashInput, agentrpc.OpBashKill, agentrpc.OpBashState, agentrpc.OpBashHistory,
		agentrpc.OpBashReplay, agentrpc.OpBashProcesses, agentrpc.OpBashGC, agentrpc.OpBashTemplates,
		agentrpc.OpBashService, agentrpc.OpBashServiceStatus, agentrpc.OpBashServiceKill, agentrpc.OpBashServiceLogs,
	}
	for _, op := range ops {
		if _, ok := handlers[op]; !ok {
			t.Errorf("no dispatch table entry for op %q", op)
		}
	}
	if len(handlers) != len(ops) {
		t.Errorf("handlers has %d entries, test lists %d — one of them drifted", len(handlers), len(ops))
	}
}

// TestBashStateProcessesGCReportInvalidParamsOnMalformedJSON pins the fix for
// the inconsistency the ticket named: these three ops used to swallow
// unmarshal errors with `_ =` while every other op reported invalid_params.
func TestBashStateProcessesGCReportInvalidParamsOnMalformedJSON(t *testing.T) {
	_, client := startTestServer(t)
	// Syntactically valid JSON that doesn't fit the target struct's field
	// types — request.Params must itself be valid JSON to go out over the
	// wire at all (json.RawMessage validates that on marshal), so a bare
	// syntax error like `{not json` never reaches the server.
	malformedByOp := map[string]json.RawMessage{
		agentrpc.OpBashState:     json.RawMessage(`{"session": 123}`),
		agentrpc.OpBashProcesses: json.RawMessage(`{"session": 123}`),
		agentrpc.OpBashGC:        json.RawMessage(`{"older_than_hours": "not-a-number"}`),
	}

	for op, malformed := range malformedByOp {
		t.Run(op, func(t *testing.T) {
			err := client.Call(context.Background(), agentrpc.Request{ID: "t", Op: op, Params: malformed}, nil)
			if err == nil {
				t.Fatal("expected an error for malformed params")
			}
			rpcErr, ok := err.(*agentrpc.Error)
			if !ok {
				t.Fatalf("expected *agentrpc.Error, got %T: %v", err, err)
			}
			if rpcErr.Code != "invalid_params" {
				t.Fatalf("error code = %q, want invalid_params", rpcErr.Code)
			}
		})
	}
}

// TestBashStateProcessesGCStillToleratesNoParams confirms the fix didn't
// take away the thing that made these params optional in the first place.
func TestBashStateProcessesGCStillToleratesNoParams(t *testing.T) {
	_, client := startTestServer(t)

	var state any
	if err := client.Call(context.Background(), agentrpc.Request{ID: "s", Op: agentrpc.OpBashState}, &state); err != nil {
		t.Fatalf("BashState with no params: %v", err)
	}
	var procs any
	if err := client.Call(context.Background(), agentrpc.Request{ID: "p", Op: agentrpc.OpBashProcesses}, &procs); err != nil {
		t.Fatalf("BashProcesses with no params: %v", err)
	}
	var gc any
	if err := client.Call(context.Background(), agentrpc.Request{ID: "g", Op: agentrpc.OpBashGC}, &gc); err != nil {
		t.Fatalf("BashGC with no params: %v", err)
	}
}

func TestDigestForStreamSelectsCorrectStream(t *testing.T) {
	inv := storage.Invocation{
		Stdout: storage.StreamRef{SHA256: "stdout-digest"},
		Stderr: storage.StreamRef{SHA256: "stderr-digest"},
	}
	if got, err := digestForStream(inv, "stdout"); err != nil || got != "stdout-digest" {
		t.Fatalf("digestForStream(stdout) = %q, %v", got, err)
	}
	if got, err := digestForStream(inv, "stderr"); err != nil || got != "stderr-digest" {
		t.Fatalf("digestForStream(stderr) = %q, %v", got, err)
	}
	if _, err := digestForStream(inv, "bogus"); err == nil {
		t.Fatal("expected an error for an unrecognized stream name")
	}
}

func TestUnknownOpReportsUnknownOperation(t *testing.T) {
	_, client := startTestServer(t)
	err := client.Call(context.Background(), agentrpc.Request{ID: "t", Op: "not_a_real_op"}, nil)
	if err == nil {
		t.Fatal("expected an error for an unknown op")
	}
	rpcErr, ok := err.(*agentrpc.Error)
	if !ok {
		t.Fatalf("expected *agentrpc.Error, got %T: %v", err, err)
	}
	if rpcErr.Code != "unknown_operation" {
		t.Fatalf("error code = %q, want unknown_operation", rpcErr.Code)
	}
}
