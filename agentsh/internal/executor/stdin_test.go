package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/agentsh/internal/storage"
)

func TestInteractivePromptTransitionsToWaitingOnInput(t *testing.T) {
	exec, store, root := testExecutor(t)
	exec.IdleWait = 150 * time.Millisecond

	// Command asks for input interactively
	inv, err := exec.Execute(context.Background(), Request{
		Command:     `read -r -p "Confirm? " answer; echo "got:$answer"`,
		CWD:         root,
		Interactive: true,
		IdleTimeout: 150 * time.Millisecond,
		Timeout:     5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inv.State != storage.StateWaitingOnInput {
		t.Fatalf("expected state waiting_on_input, got %s (reason: %v)", inv.State, inv.Reason)
	}
	if !strings.Contains(inv.Stdout.Preview, "waiting on input") || !strings.Contains(inv.Stdout.Preview, "BashInput") {
		t.Fatalf("missing recovery hint in preview:\n%s", inv.Stdout.Preview)
	}

	// Supply input via WriteInput
	if err := exec.WriteInput(inv.ID, []byte("yes\n")); err != nil {
		t.Fatal(err)
	}

	// Poll until exited
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := store.GetInvocation(context.Background(), inv.ID)
		if err == nil && got.State == storage.StateExited {
			out := readBlob(t, exec.Blobs, got.Stdout.SHA256)
			if !strings.Contains(out, "got:yes") {
				t.Fatalf("output missing input response: %q", out)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("interactive command did not exit after input")
}

func TestInteractivePromptCanBeKilled(t *testing.T) {
	exec, store, root := testExecutor(t)
	inv, err := exec.Execute(context.Background(), Request{
		Command:     `read -r line; echo "done"`,
		CWD:         root,
		Interactive: true,
		IdleTimeout: 100 * time.Millisecond,
		Timeout:     5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inv.State != storage.StateWaitingOnInput {
		t.Fatalf("expected waiting_on_input, got %s", inv.State)
	}
	if err := exec.Kill(inv.ID, 0); err != nil {
		t.Fatal(err)
	}

	// Kill only signals; wait for wait()'s finalize (blob commit, DB write)
	// to actually finish before the test returns, or its async I/O can race
	// t.TempDir()'s cleanup (flaked as exactly that in CI — GitHub Actions
	// run 32247405015: "TempDir RemoveAll cleanup: ... directory not empty").
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got, err := store.GetInvocation(context.Background(), inv.ID); err == nil &&
			(got.State == storage.StateKilled || got.State == storage.StateExited) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("invocation did not finish after kill")
}
