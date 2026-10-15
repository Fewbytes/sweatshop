package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/gitx/gittest"
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
		if _, err := gittest.Git(ctx, r.Dir, args...); err != nil {
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
	if _, err := gittest.Git(ctx, r.Dir, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := gittest.Git(ctx, r.Dir, "commit", "--quiet", "-m", "change "+name); err != nil {
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

func TestChangedBlobsListsAddedAndModifiedFilesOnly(t *testing.T) {
	r, ctx := newRepo(t)
	first := commitFile(t, r, ctx, "gone.txt", "bye")
	commitFile(t, r, ctx, "a.bin", "\x7fELF"+strings.Repeat("x", 2000))
	commitFile(t, r, ctx, "dir/b.txt", "hello")
	if _, err := gittest.Git(ctx, r.Dir, "rm", "--quiet", "gone.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := gittest.Git(ctx, r.Dir, "commit", "--quiet", "-m", "remove"); err != nil {
		t.Fatal(err)
	}
	last, err := r.Commit(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := r.ChangedBlobs(ctx, first, last)
	if err != nil {
		t.Fatal(err)
	}
	if len(blobs) != 2 || blobs[0].Path != "a.bin" || blobs[1].Path != "dir/b.txt" {
		t.Fatalf("ChangedBlobs = %+v", blobs)
	}
	if blobs[0].Size != 2004 || len(blobs[0].Head) != BlobHeadBytes || !strings.HasPrefix(string(blobs[0].Head), "\x7fELF") {
		t.Fatalf("a.bin = size %d, head %d bytes", blobs[0].Size, len(blobs[0].Head))
	}
	if blobs[1].Size != 5 || string(blobs[1].Head) != "hello" {
		t.Fatalf("dir/b.txt = %+v", blobs[1])
	}
}

func TestRefExists(t *testing.T) {
	r, ctx := newRepo(t)
	head := commitFile(t, r, ctx, "a.txt", "1")
	if ok, err := r.RefExists(ctx, "refs/line/andon"); err != nil || ok {
		t.Fatalf("RefExists(missing) = %v, %v", ok, err)
	}
	if _, err := gittest.Git(ctx, r.Dir, "update-ref", "refs/line/andon", head); err != nil {
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
	if err := r.UpdateRef(ctx, "refs/line/candidates/x", head, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteRef(ctx, "refs/line/candidates/x"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.RefExists(ctx, "refs/line/candidates/x"); ok {
		t.Fatal("ref still exists after DeleteRef")
	}
}

func TestBlobRoundTrip(t *testing.T) {
	r, ctx := newRepo(t)
	id, err := r.WriteBlob(ctx, []byte("event\n"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := r.ReadBlob(ctx, id)
	if err != nil || string(data) != "event\n" {
		t.Fatalf("ReadBlob = %q, %v", data, err)
	}
	if _, err := r.ReadBlob(ctx, "-p"); err == nil {
		t.Fatal("ReadBlob accepted an option-like id")
	}
}

func TestDeleteRefIfIsCAS(t *testing.T) {
	r, ctx := newRepo(t)
	a := commitFile(t, r, ctx, "a.txt", "1")
	b := commitFile(t, r, ctx, "a.txt", "2")
	const ref = "refs/line/andon/x"
	if err := r.UpdateRef(ctx, ref, a, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteRefIf(ctx, ref, b); !errors.Is(err, ErrRefMoved) {
		t.Fatalf("DeleteRefIf with stale value = %v, want ErrRefMoved", err)
	}
	if ok, _ := r.RefExists(ctx, ref); !ok {
		t.Fatal("ref deleted despite stale old value")
	}
	if err := r.DeleteRefIf(ctx, ref, a); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.RefExists(ctx, ref); ok {
		t.Fatal("ref still exists after matching DeleteRefIf")
	}
	if err := r.DeleteRefIf(ctx, ref, a); !errors.Is(err, ErrRefMoved) {
		t.Fatalf("DeleteRefIf on missing ref = %v, want ErrRefMoved", err)
	}
	if err := r.DeleteRefIf(ctx, "-d", a); err == nil {
		t.Fatal("DeleteRefIf accepted an option-like ref")
	}
}

func TestTrailer(t *testing.T) {
	r, ctx := newRepo(t)
	if err := os.WriteFile(filepath.Join(r.Dir, "a.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gittest.Git(ctx, r.Dir, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := gittest.Git(ctx, r.Dir, "commit", "--quiet", "-m", "fix\n\nAndon-Event: abc123\nBead: sweatshop-1\n"); err != nil {
		t.Fatal(err)
	}
	head, err := r.Commit(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Trailer(ctx, head, "Andon-Event")
	if err != nil || len(got) != 1 || got[0] != "abc123" {
		t.Fatalf("Trailer = %q, %v", got, err)
	}
	if got, err := r.Trailer(ctx, head, "Missing"); err != nil || got != nil {
		t.Fatalf("Trailer(Missing) = %q, %v", got, err)
	}
	if _, err := r.Trailer(ctx, head, "-x"); err == nil {
		t.Fatal("Trailer accepted an option-like key")
	}
	if _, err := r.Trailer(ctx, "-x", "Bead"); err == nil {
		t.Fatal("Trailer accepted an option-like commit")
	}
}

func TestUpdateRefDeletedRef(t *testing.T) {
	r, ctx := newRepo(t)
	first := commitFile(t, r, ctx, "a.txt", "1")
	second := commitFile(t, r, ctx, "a.txt", "2")

	// Create a ref and then delete it
	if err := r.UpdateRef(ctx, "refs/heads/toDelete", first, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteRef(ctx, "refs/heads/toDelete"); err != nil {
		t.Fatal(err)
	}

	// Try CAS on the deleted ref; should return ErrRefMoved
	err := r.UpdateRef(ctx, "refs/heads/toDelete", second, first)
	if !errors.Is(err, ErrRefMoved) {
		t.Fatalf("CAS on deleted ref err = %v, want ErrRefMoved", err)
	}
}

func TestUpdateRefCreateOverExisting(t *testing.T) {
	r, ctx := newRepo(t)
	first := commitFile(t, r, ctx, "a.txt", "1")
	second := commitFile(t, r, ctx, "a.txt", "2")

	// Create a ref
	if err := r.UpdateRef(ctx, "refs/heads/existing", first, ""); err != nil {
		t.Fatal(err)
	}

	// Try to create (oldValue "") over an existing ref; should return ErrRefMoved
	err := r.UpdateRef(ctx, "refs/heads/existing", second, "")
	if !errors.Is(err, ErrRefMoved) {
		t.Fatalf("create over existing ref err = %v, want ErrRefMoved", err)
	}
}

func TestGitDirIsolation(t *testing.T) {
	// Create two repos
	r1, ctx := newRepo(t)
	r2, ctx2 := newRepo(t)

	// Create a commit in r1
	head1 := commitFile(t, r1, ctx, "a.txt", "1")

	// Create a commit in r2
	_ = commitFile(t, r2, ctx2, "b.txt", "2")

	// Set GIT_DIR to point to r2's repo
	t.Setenv("GIT_DIR", filepath.Join(r2.Dir, ".git"))

	// Verify that r1.Commit still resolves r1's HEAD, not r2's
	sha, err := r1.Commit(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if sha != head1 {
		t.Fatalf("Commit with GIT_DIR set = %s, want %s", sha, head1)
	}
}

func TestTreeAndCommitTree(t *testing.T) {
	dir := t.TempDir()
	r := Repo{Dir: dir}
	ctx := context.Background()
	if _, err := gittest.Git(ctx, r.Dir, "init", "--quiet", "--bare"); err != nil {
		t.Fatal(err)
	}
	blob, err := r.WriteBlob(ctx, []byte("notes"))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := r.WriteTreeWithBlob(ctx, "testament", blob)
	if err != nil {
		t.Fatal(err)
	}
	c1, err := r.CommitTree(ctx, tree, "", "one", "viceroy")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := r.CommitTree(ctx, tree, c1, "two", "viceroy")
	if err != nil {
		t.Fatal(err)
	}
	if ps, err := r.Parents(ctx, c2); err != nil || len(ps) != 1 || ps[0] != c1 {
		t.Fatalf("parents %v %v", ps, err)
	}
	if got, err := r.ReadBlob(ctx, c2+":testament"); err != nil || string(got) != "notes" {
		t.Fatalf("blob %q %v", got, err)
	}
	if _, err := r.WriteTreeWithBlob(ctx, "a/b", blob); err == nil {
		t.Fatal("bad name accepted")
	}
	if _, err := r.CommitTree(ctx, "-x", "", "m", "v"); err == nil {
		t.Fatal("option accepted")
	}
}
