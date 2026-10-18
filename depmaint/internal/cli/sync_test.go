package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordingRunner plays git and dolt: it records each command line and
// materializes .dolt on `dolt init`.
type recordingRunner struct {
	calls   []string
	remotes string
	failOn  string
}

func (r *recordingRunner) run(_ context.Context, dir, name string, args ...string) (string, error) {
	line := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, line)
	if r.failOn != "" && strings.HasPrefix(line, r.failOn) {
		return "", errors.New("boom")
	}
	switch line {
	case "git remote get-url origin":
		return "git@example.com:team/repo.git\n", nil
	case "dolt init":
		return "", os.MkdirAll(filepath.Join(dir, ".dolt"), 0o755)
	case "dolt remote -v":
		return r.remotes, nil
	}
	return "", nil
}

func TestSyncFirstRunInitsAddsRemoteUnderDepmaintRefThenPullsAndPushes(t *testing.T) {
	root := t.TempDir()
	r := &recordingRunner{}
	var out bytes.Buffer
	if err := Sync(context.Background(), root, r.run, &out); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	want := []string{
		"git remote get-url origin",
		"dolt init",
		"dolt remote -v",
		"dolt remote add --ref refs/dolt/depmaint/data origin git@example.com:team/repo.git",
		"dolt pull origin main",
		"dolt push origin main",
	}
	if strings.Join(r.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands:\n%s\nwant:\n%s", strings.Join(r.calls, "\n"), strings.Join(want, "\n"))
	}
	for _, c := range r.calls {
		if strings.Contains(c, "refs/dolt/data") {
			t.Fatalf("must never touch beads' ref: %q", c)
		}
	}
}

func TestSyncSecondRunSkipsInitAndRemoteAdd(t *testing.T) {
	root := t.TempDir()
	first := &recordingRunner{}
	if err := Sync(context.Background(), root, first.run, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	second := &recordingRunner{remotes: "origin git@example.com:team/repo.git\n"}
	if err := Sync(context.Background(), root, second.run, &bytes.Buffer{}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	for _, c := range second.calls {
		if c == "dolt init" || strings.HasPrefix(c, "dolt remote add") {
			t.Errorf("second sync repeated one-time setup: %q", c)
		}
	}
}

func TestSyncPullFailureAbortsBeforePush(t *testing.T) {
	r := &recordingRunner{failOn: "dolt pull"}
	err := Sync(context.Background(), t.TempDir(), r.run, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want pull error, got %v", err)
	}
	for _, c := range r.calls {
		if strings.HasPrefix(c, "dolt push") {
			t.Fatal("pushed after a failed pull")
		}
	}
}
