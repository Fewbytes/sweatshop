package storage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func sampleInvocation() Invocation {
	env := map[string]string{}
	for _, name := range []string{"PATH", "HOME", "SSH_AUTH_SOCK", "TURSO_AUTH_TOKEN", "LANG", "TERM"} {
		env[name] = strings.Repeat("v", 64)
	}
	ended := time.Now().UTC()
	exit := 0
	return Invocation{
		ID: "inv_1", Session: "default", Command: "echo hi", State: StateExited,
		Argv: []string{"bash", "-c", "echo hi"}, CWD: "/tmp", ExitCode: &exit,
		StartedAt: ended.Add(-time.Second), EndedAt: &ended, DurationMS: 1000,
		EnvAfter: env, EnvDelta: map[string]string{"FOO": "bar"},
		PathsTouched: []string{"/tmp/a"},
		Stdout:       StreamRef{SHA256: "abc", Bytes: 3, Lines: 1, Preview: "hi\n"},
	}
}

// The whole point of the projection: the environment snapshot must not ride
// along on every response.
func TestViewOmitsEnvironmentSnapshot(t *testing.T) {
	encoded, err := json.Marshal(sampleInvocation().View())
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"SSH_AUTH_SOCK", "TURSO_AUTH_TOKEN", "EnvAfter", "env_after"} {
		if strings.Contains(string(encoded), leaked) {
			t.Errorf("view leaks %q: %s", leaked, encoded)
		}
	}
}

func TestViewKeepsWhatTheAgentNeeds(t *testing.T) {
	encoded, err := json.Marshal(sampleInvocation().View())
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"id", "state", "exit_code", "duration_ms", "stdout", "stderr", "env_delta", "command"} {
		if _, ok := decoded[required]; !ok {
			t.Errorf("view drops %q, which the agent needs to act on the result", required)
		}
	}
}

func TestViewIsSubstantiallySmallerThanTheRecord(t *testing.T) {
	invocation := sampleInvocation()
	full, err := json.Marshal(invocation)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := json.Marshal(invocation.View())
	if err != nil {
		t.Fatal(err)
	}
	if len(projected) >= len(full) {
		t.Errorf("view (%d bytes) is no smaller than the record (%d bytes)", len(projected), len(full))
	}
}

func TestViewsProjectsEveryRow(t *testing.T) {
	views := Views([]Invocation{sampleInvocation(), sampleInvocation()})
	if len(views) != 2 {
		t.Fatalf("got %d views, want 2", len(views))
	}
	encoded, err := json.Marshal(views)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "TURSO_AUTH_TOKEN") {
		t.Error("history projection leaks the environment snapshot")
	}
}
