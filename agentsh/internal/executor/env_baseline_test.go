//go:build unix

package executor

import (
	"context"
	"testing"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/storage"
)

// A fresh session's first command must not report the inherited environment as
// its own changes. Before the baseline was seeded, this delta was the entire
// environment — larger than most commands' output.
func TestFirstCommandInSessionReportsNoInheritedEnvironment(t *testing.T) {
	exec, _, root := testExecutor(t)
	invocation, err := exec.Execute(context.Background(), Request{
		Command: "echo hello", Session: "fresh", CWD: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if invocation.State != storage.StateExited {
		t.Fatalf("state = %s", invocation.State)
	}
	for _, inherited := range []string{"PATH", "HOME", "TERM", "LANG"} {
		if _, found := invocation.EnvDelta[inherited]; found {
			t.Errorf("EnvDelta reports inherited %q as a change made by the command", inherited)
		}
	}
	if len(invocation.EnvDelta) > 8 {
		t.Errorf("EnvDelta has %d entries for `echo hello`: %v", len(invocation.EnvDelta), invocation.EnvDelta)
	}
}

// A variable the command actually exports must still surface.
func TestExportedVariableAppearsInDelta(t *testing.T) {
	exec, _, root := testExecutor(t)
	invocation, err := exec.Execute(context.Background(), Request{
		Command: "export AGENTSH_TEST_VAR=present", Session: "fresh", CWD: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if invocation.EnvDelta["AGENTSH_TEST_VAR"] != "present" {
		t.Errorf("exported variable missing from delta: %v", invocation.EnvDelta)
	}
}
