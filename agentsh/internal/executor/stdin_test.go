package executor

import (
	"context"
	"github.com/Fewbytes/sweatshop/agentsh/internal/testwait"
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
	var got storage.Invocation
	testwait.Until(t, 2*time.Second, "interactive command to exit after input", func() bool {
		var err error
		got, err = store.GetInvocation(context.Background(), inv.ID)
		return err == nil && got.State == storage.StateExited
	})
	out := readBlob(t, exec.Blobs, got.Stdout.SHA256)
	if !strings.Contains(out, "got:yes") {
		t.Fatalf("output missing input response: %q", out)
	}
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
	testwait.Until(t, 10*time.Second, "invocation to finish after kill", func() bool {
		got, err := store.GetInvocation(context.Background(), inv.ID)
		return err == nil && (got.State == storage.StateKilled || got.State == storage.StateExited)
	})
}
