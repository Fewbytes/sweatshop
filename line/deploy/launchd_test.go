package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recExec struct {
	calls []string
	build func(argv []string)
}

func (r *recExec) Run(_ context.Context, _ string, argv ...string) error {
	r.calls = append(r.calls, strings.Join(argv, " "))
	if r.build != nil {
		r.build(argv)
	}
	return nil
}

func newLaunchd(t *testing.T) (Launchd, *recExec) {
	t.Helper()
	ex := &recExec{}
	return Launchd{
		Dir: t.TempDir(), Source: "/line/remote.git", Domain: "gui/501",
		Labels: []string{"com.example.line.serve", "com.example.line.andon"}, Exec: ex,
	}, ex
}

func TestLaunchdNeverRunsOutsideAllowlist(t *testing.T) {
	l, ex := newLaunchd(t)
	ctx := context.Background()
	for _, argv := range [][]string{
		{"launchctl", "kickstart", "-k", "gui/501/com.example.other"},
		{"launchctl", "kickstart", "-k", "system/com.example.line.serve"},
		{"launchctl", "kickstart", "com.example.line.serve"},
		{"launchctl", "kickstart", "-k", "gui/501/com.example.line.serve", "extra"},
		{"launchctl", "bootout", "gui/501/com.example.line.serve"},
		{"launchctl", "load", "/Library/LaunchDaemons/x.plist"},
		{"sh", "-c", "id"},
		{"rm", "-rf", "/"},
		{"git", "fetch", "--quiet", "--no-tags", "https://evil.example/x.git", "refs/heads/master"},
		{"git", "checkout", "--quiet", "--detach", "--force", c1},
		{"git", "checkout", "--quiet", "--detach", "master"},
		{"git", "-c", "core.sshCommand=x", "init", "--quiet"},
		{"go", "build", "-o", "/usr/local/bin/sweatshop", "./cmd/sweatshop"},
		{"go", "build", "-o", l.Dir + "/x", "./cmd/evil"},
		{"go", "run", "./cmd/sweatshop"},
		{},
	} {
		if err := l.run(ctx, "", argv...); err == nil {
			t.Errorf("ran %q", argv)
		}
	}
	if len(ex.calls) != 0 {
		t.Fatalf("executed: %v", ex.calls)
	}
	for _, argv := range [][]string{
		{"launchctl", "kickstart", "-k", "gui/501/com.example.line.serve"},
		{"git", "init", "--quiet"},
		{"git", "checkout", "--quiet", "--detach", c1},
		{"go", "build", "-o", l.Dir + "/" + c1 + "/sweatshop.tmp", "./cmd/sweatshop"},
	} {
		if !l.Allowed(argv) {
			t.Errorf("legitimate argv refused: %q", argv)
		}
	}
}

func TestLaunchdApplyAndRollback(t *testing.T) {
	l, ex := newLaunchd(t)
	ctx := context.Background()
	ex.build = func(argv []string) {
		if argv[0] == "go" {
			_ = os.WriteFile(argv[3], []byte("bin"), 0o755)
		}
	}
	if cur, err := l.Current(ctx); err != nil || cur.Commit != "" {
		t.Fatalf("fresh: %+v %v", cur, err)
	}
	if err := l.Apply(ctx, Release{Commit: c1}); err != nil {
		t.Fatal(err)
	}
	if err := l.Apply(ctx, Release{Commit: c2}); err != nil {
		t.Fatal(err)
	}
	cur, _ := l.Current(ctx)
	if cur.Commit != c2 {
		t.Fatalf("current %+v", cur)
	}
	if target, _ := os.Readlink(filepath.Join(l.Dir, "current")); target != filepath.Join(l.Dir, c2, "sweatshop") {
		t.Fatalf("symlink %s", target)
	}
	ex.calls = nil
	if err := l.Rollback(ctx, Release{Commit: c1}); err != nil {
		t.Fatal(err)
	}
	if cur, _ := l.Current(ctx); cur.Commit != c1 {
		t.Fatalf("after rollback %+v", cur)
	}
	if len(ex.calls) != 2 || ex.calls[0] != "launchctl kickstart -k gui/501/com.example.line.serve" {
		t.Fatalf("rollback calls %v", ex.calls)
	}
	if err := l.Rollback(ctx, Release{}); err == nil {
		t.Fatal("rollback with no previous release")
	}
	if err := l.Apply(ctx, Release{Commit: "master"}); err == nil {
		t.Fatal("applied a non-commit")
	}
}
