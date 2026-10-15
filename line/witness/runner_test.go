package witness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/internal/testwait"
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

var checkName = regexp.MustCompile(`--name '(sweatshop-check-[0-9a-f]{16})' `)

func TestContainerRunnerWrapsCommandThroughInner(t *testing.T) {
	inner := &recordingRunner{}
	var removed []string
	runner := NewRunner(constitution.Runner{Kind: "container", Engine: "docker", Image: "ci:1"}, inner)
	cr := runner.(ContainerRunner)
	cr.Remove = func(name string) { removed = append(removed, name) }
	result, err := cr.Run(context.Background(), "/tmp/check out", "echo 'hi' && just ci")
	if err != nil || result.EvidenceID != "inv_rec" {
		t.Fatalf("result = %+v, %v", result, err)
	}
	m := checkName.FindStringSubmatch(inner.command)
	if m == nil {
		t.Fatalf("no unique --name in %s", inner.command)
	}
	want := `docker run --name '` + m[1] + `' --cap-drop=ALL --security-opt=no-new-privileges --memory '4g' --pids-limit 1024 --cpus '2' -v '/tmp/check out':/src -w /src -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=/src 'ci:1' bash -c 'echo '\''hi'\'' && just ci'`
	if inner.command != want {
		t.Fatalf("wrapped command:\n got %s\nwant %s", inner.command, want)
	}
	if strings.Contains(inner.command, "--rm") {
		t.Fatalf("wrapped command has --rm, which deletes the container before OOMKilled is inspected: %s", inner.command)
	}
	if inner.dir != "/tmp/check out" {
		t.Fatalf("inner dir = %q", inner.dir)
	}
	if len(removed) != 1 || removed[0] != m[1] {
		t.Fatalf("removed = %v, want [%s]", removed, m[1])
	}

	// Names are unique per run so concurrent stations never collide.
	if _, err := cr.Run(context.Background(), "/x", "true"); err != nil {
		t.Fatal(err)
	}
	if m2 := checkName.FindStringSubmatch(inner.command); m2 == nil || m2[1] == m[1] {
		t.Fatalf("second run reused name: %s", inner.command)
	}
}

func TestContainerRunnerUsesConstitutionLimits(t *testing.T) {
	inner := &recordingRunner{}
	runner := NewRunner(constitution.Runner{Kind: "container", Engine: "podman", Image: "ci:1", Memory: "512m", Pids: 64, CPUs: "0.5"}, inner)
	cr := runner.(ContainerRunner)
	cr.Remove = func(string) {}
	if _, err := cr.Run(context.Background(), "/x", "true"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inner.command, "--memory '512m' --pids-limit 64 --cpus '0.5'") || !strings.HasPrefix(inner.command, "podman run ") {
		t.Fatalf("limits not applied: %s", inner.command)
	}
}

type failingRunner struct{ command string }

func (f *failingRunner) Run(_ context.Context, _, command string) (RunResult, error) {
	f.command = command
	return RunResult{}, errors.New("cancelled")
}

func TestContainerRunnerRemovesContainerWhenRunFails(t *testing.T) {
	inner := &failingRunner{}
	var removed []string
	cr := ContainerRunner{Inner: inner, Engine: "docker", Image: "ci:1", Remove: func(name string) { removed = append(removed, name) }}
	if _, err := cr.Run(context.Background(), "/x", "make build"); err == nil {
		t.Fatal("inner error swallowed")
	}
	m := checkName.FindStringSubmatch(inner.command)
	if m == nil || len(removed) != 1 || removed[0] != m[1] {
		t.Fatalf("removed = %v after failed run of %s", removed, inner.command)
	}
}

func TestLogRunnerCancelIsAnErrorNotAResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	never := filepath.Join(dir, "never-written")
	if err := syscall.Mkfifo(never, 0o600); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(dir, "started")
	go func() {
		testwait.Until(t, 5*time.Second, "command to start", func() bool {
			_, err := os.Stat(started)
			return err == nil
		})
		cancel()
	}()
	res, err := LogRunner{Dir: t.TempDir()}.Run(ctx, t.TempDir(), "touch '"+started+"'; cat '"+never+"'")
	if err == nil {
		t.Fatalf("cancelled run reported a result: %+v", res)
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

type exitRunner struct{ code int }

func (e exitRunner) Run(context.Context, string, string) (RunResult, error) {
	return RunResult{ExitCode: e.code, EvidenceID: "inv_x"}, nil
}

func TestContainerRunnerEngineFailuresAreErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		code    int
		oom     bool
		oomErr  error
		wantErr bool
	}{
		{"engine could not start", 125, false, nil, true},
		{"killed by engine or VM", 137, false, nil, true},
		{"inspect failed", 137, false, errors.New("no such container"), true},
		{"oom by own limit", 137, true, nil, false},
		{"plain failure", 1, false, nil, false},
		{"pass", 0, false, nil, false},
	} {
		inspected := false
		cr := ContainerRunner{Inner: exitRunner{tc.code}, Engine: "docker", Image: "ci:1",
			Remove:    func(string) {},
			OOMKilled: func(string) (bool, error) { inspected = true; return tc.oom, tc.oomErr }}
		res, err := cr.Run(context.Background(), "/x", "true")
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, want error %v", tc.name, err, tc.wantErr)
		}
		if !tc.wantErr && res.ExitCode != tc.code {
			t.Errorf("%s: result = %+v", tc.name, res)
		}
		if inspected != (tc.code == 137) {
			t.Errorf("%s: inspected = %v", tc.name, inspected)
		}
	}
}
