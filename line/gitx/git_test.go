package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newRepo(t *testing.T) (Repo, context.Context) {
	t.Helper()
	ctx := context.Background()
	r := Repo{Dir: t.TempDir()}
	for _, args := range [][]string{
		{"init", "--quiet", "--initial-branch=master"},
		{"config", "user.name", "test"},
		{"config", "user.email", "test@example.com"},
	} {
		if _, err := r.Git(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	return r, ctx
}

func commitFile(t *testing.T, r Repo, ctx context.Context, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(r.Dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Git(ctx, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Git(ctx, "commit", "--quiet", "-m", "change "+name); err != nil {
		t.Fatal(err)
	}
	sha, err := r.Commit(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

func TestCommitTreeParents(t *testing.T) {
	r, ctx := newRepo(t)
	first := commitFile(t, r, ctx, "a.txt", "1")
	second := commitFile(t, r, ctx, "b.txt", "2")
	parents, err := r.Parents(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if len(parents) != 1 || parents[0] != first {
		t.Fatalf("Parents = %v, want [%s]", parents, first)
	}
	if roots, _ := r.Parents(ctx, first); len(roots) != 0 {
		t.Fatalf("root commit has parents %v", roots)
	}
	tree, err := r.Tree(ctx, second)
	if err != nil || len(tree) != 40 {
		t.Fatalf("Tree = %q, %v", tree, err)
	}
	if _, err := r.Commit(ctx, "no-such-branch"); err == nil {
		t.Fatal("Commit resolved a missing revision")
	}
}

func TestChangedPaths(t *testing.T) {
	r, ctx := newRepo(t)
	first := commitFile(t, r, ctx, "a.txt", "1")
	commitFile(t, r, ctx, "dir/b.txt", "2")
	last := commitFile(t, r, ctx, "a.txt", "3")
	paths, err := r.ChangedPaths(ctx, first, last)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "a.txt" || paths[1] != "dir/b.txt" {
		t.Fatalf("ChangedPaths = %v", paths)
	}
	if none, _ := r.ChangedPaths(ctx, last, last); len(none) != 0 {
		t.Fatalf("ChangedPaths(x, x) = %v", none)
	}
}

func TestRefExists(t *testing.T) {
	r, ctx := newRepo(t)
	head := commitFile(t, r, ctx, "a.txt", "1")
	if ok, err := r.RefExists(ctx, "refs/line/andon"); err != nil || ok {
		t.Fatalf("RefExists(missing) = %v, %v", ok, err)
	}
	if _, err := r.Git(ctx, "update-ref", "refs/line/andon", head); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.RefExists(ctx, "refs/line/andon"); err != nil || !ok {
		t.Fatalf("RefExists(present) = %v, %v", ok, err)
	}
}

func TestUpdateRefCompareAndSwap(t *testing.T) {
	r, ctx := newRepo(t)
	first := commitFile(t, r, ctx, "a.txt", "1")
	second := commitFile(t, r, ctx, "a.txt", "2")
	third := commitFile(t, r, ctx, "a.txt", "3")
	if err := r.UpdateRef(ctx, "refs/heads/target", first, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateRef(ctx, "refs/heads/target", second, first); err != nil {
		t.Fatal(err)
	}
	err := r.UpdateRef(ctx, "refs/heads/target", third, first)
	if !errors.Is(err, ErrRefMoved) {
		t.Fatalf("stale CAS err = %v, want ErrRefMoved", err)
	}
	if got, _ := r.Ref(ctx, "refs/heads/target"); got != second {
		t.Fatalf("ref = %s after failed CAS, want %s", got, second)
	}
}

func TestDeleteRef(t *testing.T) {
	r, ctx := newRepo(t)
	head := commitFile(t, r, ctx, "a.txt", "1")
	r.UpdateRef(ctx, "refs/line/candidates/x", head, "")
	if err := r.DeleteRef(ctx, "refs/line/candidates/x"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.RefExists(ctx, "refs/line/candidates/x"); ok {
		t.Fatal("ref still exists after DeleteRef")
	}
}
