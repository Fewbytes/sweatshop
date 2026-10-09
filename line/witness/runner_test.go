package witness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/constitution"
)

type recordingRunner struct {
	dir, command string
}

func (r *recordingRunner) Run(_ context.Context, dir, command string) (RunResult, error) {
	r.dir, r.command = dir, command
	return RunResult{ExitCode: 0, EvidenceID: "inv_rec"}, nil
}

func TestExecRunnerReportsExitCode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	passed, err := ExecRunner{}.Run(context.Background(), dir, "test -f marker")
	if err != nil || passed.ExitCode != 0 {
		t.Fatalf("run in dir = %+v, %v", passed, err)
	}
	failed, err := ExecRunner{}.Run(context.Background(), dir, "exit 3")
	if err != nil || failed.ExitCode != 3 {
		t.Fatalf("failing run = %+v, %v", failed, err)
	}
}

func TestContainerRunnerWrapsCommandThroughInner(t *testing.T) {
	inner := &recordingRunner{}
	runner := NewRunner(constitution.Runner{Kind: "container", Engine: "docker", Image: "ci:1"}, inner)
	result, err := runner.Run(context.Background(), "/tmp/check out", "echo 'hi' && just ci")
	if err != nil || result.EvidenceID != "inv_rec" {
		t.Fatalf("result = %+v, %v", result, err)
	}
	want := `docker run --rm -v '/tmp/check out':/src -w /src -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=/src 'ci:1' bash -c 'echo '\''hi'\'' && just ci'`
	if inner.command != want {
		t.Fatalf("wrapped command:\n got %s\nwant %s", inner.command, want)
	}
	if inner.dir != "/tmp/check out" {
		t.Fatalf("inner dir = %q", inner.dir)
	}
}

func TestNewRunnerLocalIsHostRunner(t *testing.T) {
	inner := &recordingRunner{}
	runner := NewRunner(constitution.Runner{Kind: "local"}, inner)
	if _, err := runner.Run(context.Background(), "/x", "true"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(inner.command, "docker") || inner.command != "true" {
		t.Fatalf("local runner wrapped the command: %q", inner.command)
	}
}
