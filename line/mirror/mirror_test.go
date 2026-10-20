package mirror

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/gitx/gittest"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gittest.Git(context.Background(), dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// lineWithMirror returns the line's remote, a bare mirror and a constitution
// declaring that mirror.
func lineWithMirror(t *testing.T) (line gitx.Repo, mirrorDir string, c constitution.Constitution) {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "master", "line")
	line = gitx.Repo{Dir: filepath.Join(dir, "line")}
	git(t, line.Dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "one")
	mirrorDir = filepath.Join(dir, "github.git")
	git(t, dir, "init", "-q", "--bare", mirrorDir)
	c = constitution.Constitution{Remotes: []constitution.Remote{
		{Name: "line", Kind: constitution.RemoteAuthoritative},
		{Name: "github", Kind: constitution.RemoteStorage, URL: mirrorDir},
	}}
	return line, mirrorDir, c
}

func TestPublishFastForwardsMasterTagsAndRefusesDivergence(t *testing.T) {
	line, mirrorDir, c := lineWithMirror(t)
	git(t, line.Dir, "tag", "v1")
	if err := Publish(context.Background(), line, c, nil); err != nil {
		t.Fatal(err)
	}
	want := git(t, line.Dir, "rev-parse", "master")
	if got := git(t, mirrorDir, "rev-parse", "master"); got != want {
		t.Fatalf("mirror master = %s, want %s", got, want)
	}
	if got := git(t, mirrorDir, "rev-parse", "v1"); got != want {
		t.Fatalf("mirror tag v1 = %s, want %s", got, want)
	}

	git(t, line.Dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "two")
	if err := Publish(context.Background(), line, c, nil); err != nil {
		t.Fatalf("fast-forward publish: %v", err)
	}
	if got, want := git(t, mirrorDir, "rev-parse", "master"), git(t, line.Dir, "rev-parse", "master"); got != want {
		t.Fatalf("mirror master after fast-forward = %s, want %s", got, want)
	}

	// Someone rewrote the line's history: the mirror must keep its commit.
	kept := git(t, mirrorDir, "rev-parse", "master")
	git(t, line.Dir, "reset", "-q", "--hard", "HEAD~1")
	git(t, line.Dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "other two")
	err := Publish(context.Background(), line, c, nil)
	if err == nil || !strings.Contains(err.Error(), `mirror "github"`) {
		t.Fatalf("diverged publish error = %v, want one naming mirror github", err)
	}
	if got := git(t, mirrorDir, "rev-parse", "master"); got != kept {
		t.Fatalf("mirror master moved to %s on a non-fast-forward, want %s", got, kept)
	}
}

func TestSSHAuthFailsWithoutDeployKey(t *testing.T) {
	if _, err := SSHAuth(t.TempDir()); err == nil || !strings.Contains(err.Error(), "deploy key") {
		t.Fatalf("SSHAuth on empty dir = %v, want a deploy key error", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DeployKeyFile), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SSHAuth(dir); err == nil {
		t.Fatal("SSHAuth accepted a malformed deploy key")
	}
}
