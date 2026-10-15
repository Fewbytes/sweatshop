package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/gitx/gittest"
)

func mustGit(t *testing.T, ctx context.Context, dir string, args ...string) string {
	t.Helper()
	out, err := gittest.Git(ctx, dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFetchMirrorsRefsAndPrunesRefsTheSourceLost(t *testing.T) {
	src, ctx := newRepo(t)
	first := commitFile(t, src, ctx, "a.txt", "1")
	second := commitFile(t, src, ctx, "a.txt", "2")
	mustGit(t, ctx, src.Dir, "branch", "topic", first)

	dst := Repo{Dir: t.TempDir()}
	if err := dst.Init(ctx, true); err != nil {
		t.Fatal(err)
	}
	specs := []string{"+HEAD:refs/source/HEAD", "+refs/heads/*:refs/source/heads/*"}
	if err := dst.Fetch(ctx, src, true, specs...); err != nil {
		t.Fatal(err)
	}
	for ref, want := range map[string]string{
		"refs/source/HEAD": second, "refs/source/heads/master": second, "refs/source/heads/topic": first,
	} {
		if got, err := dst.Ref(ctx, ref); err != nil || got != want {
			t.Fatalf("%s = %q, %v; want %s", ref, got, err, want)
		}
	}
	mustGit(t, ctx, dst.Dir, "fsck", "--strict")

	mustGit(t, ctx, src.Dir, "branch", "-D", "topic")
	if err := dst.Fetch(ctx, src, true, specs...); err != nil {
		t.Fatal(err)
	}
	if ok, _ := dst.RefExists(ctx, "refs/source/heads/topic"); ok {
		t.Fatal("prune kept a ref the source deleted")
	}
	if err := dst.Fetch(ctx, src, true, "+refs/heads/absent:refs/x/absent"); err == nil {
		t.Fatal("Fetch accepted a refspec the source cannot satisfy")
	}
}

func TestPushCommitCreatesRefOnce(t *testing.T) {
	src, ctx := newRepo(t)
	first := commitFile(t, src, ctx, "a.txt", "1")
	second := commitFile(t, src, ctx, "a.txt", "2")
	dst := Repo{Dir: t.TempDir()}
	if err := dst.Init(ctx, true); err != nil {
		t.Fatal(err)
	}
	const ref = "refs/line/candidates/x"
	if err := src.PushCommit(ctx, dst, first, ref); err != nil {
		t.Fatal(err)
	}
	if err := src.PushCommit(ctx, dst, first, ref); err != nil {
		t.Fatalf("pushing the same commit twice: %v", err)
	}
	if err := src.PushCommit(ctx, dst, second, ref); !errors.Is(err, ErrRefMoved) {
		t.Fatalf("pushing another commit over %s: %v, want ErrRefMoved", ref, err)
	}
	mustGit(t, ctx, dst.Dir, "fsck", "--strict")
}

func TestCloneBranchBareKeepsOnlyThatBranch(t *testing.T) {
	src, ctx := newRepo(t)
	head := commitFile(t, src, ctx, "a.txt", "1")
	mustGit(t, ctx, src.Dir, "branch", "other")
	dst := Repo{Dir: filepath.Join(t.TempDir(), "line.git")}
	if err := dst.CloneBranchBare(ctx, src, "refs/heads/master"); err != nil {
		t.Fatal(err)
	}
	if got, err := dst.Commit(ctx, "HEAD"); err != nil || got != head {
		t.Fatalf("HEAD = %q, %v; want %s", got, err, head)
	}
	if ok, _ := dst.RefExists(ctx, "refs/heads/other"); ok {
		t.Fatal("clone carried a branch it was not asked for")
	}
	if got := mustGit(t, ctx, dst.Dir, "rev-parse", "--is-bare-repository"); got != "true" {
		t.Fatalf("clone is not bare: %s", got)
	}
}

func TestCheckoutDetachedMaterialisesFilesAndHistory(t *testing.T) {
	src, ctx := newRepo(t)
	first := commitFile(t, src, ctx, "a.txt", "1")
	if err := os.WriteFile(filepath.Join(src.Dir, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, ctx, src.Dir, "add", "-A")
	mustGit(t, ctx, src.Dir, "commit", "--quiet", "-m", "script")
	head, _ := src.Commit(ctx, "HEAD")

	dst := Repo{Dir: t.TempDir()}
	if err := dst.CheckoutDetached(ctx, src, head); err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, ctx, dst.Dir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD = %s, want %s", got, head)
	}
	if got := mustGit(t, ctx, dst.Dir, "status", "--porcelain"); got != "" {
		t.Fatalf("checkout is not clean:\n%s", got)
	}
	if info, err := os.Stat(filepath.Join(dst.Dir, "run.sh")); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("run.sh lost its executable bit: %v %v", info, err)
	}
	if got, err := dst.Parents(ctx, head); err != nil || !slices.Equal(got, []string{first}) {
		t.Fatalf("history missing: %v, %v", got, err)
	}
}

// lineAndAgent returns a bare "line" holding one commit and an agent clone
// that has added two commits on top, as the client and witness see them.
func lineAndAgent(t *testing.T, ctx context.Context) (line, agent Repo, base, tip string) {
	t.Helper()
	line = Repo{Dir: filepath.Join(t.TempDir(), "line.git")}
	mustGit(t, ctx, filepath.Dir(line.Dir), "init", "--quiet", "--bare", "--initial-branch=master", line.Dir)
	agent = Repo{Dir: filepath.Join(t.TempDir(), "agent")}
	mustGit(t, ctx, filepath.Dir(agent.Dir), "init", "--quiet", "--initial-branch=master", agent.Dir)
	mustGit(t, ctx, agent.Dir, "config", "user.name", "test")
	mustGit(t, ctx, agent.Dir, "config", "user.email", "test@example.com")
	base = commitFile(t, agent, ctx, "a.txt", "1")
	mustGit(t, ctx, agent.Dir, "push", "--quiet", line.Dir, "HEAD:refs/heads/master")
	mustGit(t, ctx, agent.Dir, "fetch", "--quiet", line.Dir, "+refs/heads/master:refs/remotes/origin/master")
	commitFile(t, agent, ctx, "dir/b.txt", "2")
	tip = commitFile(t, agent, ctx, "a.txt", "3")
	return line, agent, base, tip
}

func TestBundleRoundTripIsReadableByGitAndStoresOnlyTheNewHistory(t *testing.T) {
	ctx := context.Background()
	line, agent, base, tip := lineAndAgent(t, ctx)
	cache := Repo{Dir: t.TempDir()}
	if err := cache.Init(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := cache.Fetch(ctx, line, true, "+refs/heads/*:refs/line/heads/*"); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "candidate.bundle")
	if err := agent.CreateBundle(ctx, path, "refs/sweatshop/candidate", tip, base); err != nil {
		t.Fatal(err)
	}
	mustGit(t, ctx, cache.Dir, "bundle", "verify", path)

	if err := cache.FetchBundle(ctx, path, "refs/line/heads/", false, "+refs/sweatshop/*:refs/bundle/n/*"); err != nil {
		t.Fatal(err)
	}
	if got, err := cache.Ref(ctx, "refs/bundle/n/candidate"); err != nil || got != tip {
		t.Fatalf("bundle ref = %q, %v; want %s", got, err, tip)
	}
	mustGit(t, ctx, cache.Dir, "fsck", "--strict")
	if got := mustGit(t, ctx, cache.Dir, "rev-list", "--count", base+".."+tip); got != "2" {
		t.Fatalf("%s..%s has %s commits, want 2", base, tip, got)
	}
}

func TestFetchBundleRejectsPrerequisiteTheLineLacks(t *testing.T) {
	ctx := context.Background()
	_, agent, _, tip := lineAndAgent(t, ctx)
	orphan := commitFile(t, agent, ctx, "c.txt", "4")
	path := filepath.Join(t.TempDir(), "candidate.bundle")
	if err := agent.CreateBundle(ctx, path, "refs/sweatshop/candidate", orphan, tip); err != nil {
		t.Fatal(err)
	}
	fresh := Repo{Dir: t.TempDir()}
	if err := fresh.Init(ctx, true); err != nil {
		t.Fatal(err)
	}
	err := fresh.FetchBundle(ctx, path, "refs/line/heads/", false, "+refs/sweatshop/*:refs/bundle/*")
	if err == nil {
		t.Fatal("FetchBundle accepted a bundle whose prerequisite is missing")
	}
	if refs, _ := fresh.RefNames(ctx, "refs/"); len(refs) != 0 {
		t.Fatalf("a rejected bundle left refs behind: %v", refs)
	}
}

func TestFetchBundleRejectsTruncatedHistory(t *testing.T) {
	ctx := context.Background()
	line, agent, _, tip := lineAndAgent(t, ctx)
	path := filepath.Join(t.TempDir(), "candidate.bundle")
	// No prerequisite: the bundle carries everything, so dropping one object
	// leaves a ref whose history is incomplete.
	if err := agent.CreateBundle(ctx, path, "refs/sweatshop/candidate", tip, ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	truncated := filepath.Join(t.TempDir(), "truncated.bundle")
	if err := os.WriteFile(truncated, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := line.FetchBundle(ctx, truncated, "refs/line/heads/", false, "+refs/sweatshop/*:refs/bundle/*"); err == nil {
		t.Fatal("FetchBundle accepted a truncated pack")
	}
}

func TestCreateBundleNeedsOnlyHistoryDownToTheBaseSoShallowClonesWork(t *testing.T) {
	ctx := context.Background()
	line, agent, base, tip := lineAndAgent(t, ctx)
	shallow := Repo{Dir: filepath.Join(t.TempDir(), "shallow")}
	mustGit(t, ctx, filepath.Dir(shallow.Dir), "clone", "--quiet", "--depth", "1", "file://"+line.Dir, shallow.Dir)
	mustGit(t, ctx, shallow.Dir, "config", "user.name", "test")
	mustGit(t, ctx, shallow.Dir, "config", "user.email", "test@example.com")
	mustGit(t, ctx, shallow.Dir, "fetch", "--quiet", "--depth", "1", agent.Dir, tip)
	mustGit(t, ctx, shallow.Dir, "checkout", "--quiet", "-b", "work", base)
	next := commitFile(t, shallow, ctx, "s.txt", "x")

	path := filepath.Join(t.TempDir(), "candidate.bundle")
	if err := shallow.CreateBundle(ctx, path, "refs/sweatshop/candidate", next, base); err != nil {
		t.Fatal(err)
	}
	cache := Repo{Dir: t.TempDir()}
	if err := cache.Init(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := cache.Fetch(ctx, line, true, "+refs/heads/*:refs/line/heads/*"); err != nil {
		t.Fatal(err)
	}
	if err := cache.FetchBundle(ctx, path, "refs/line/heads/", false, "+refs/sweatshop/*:refs/bundle/*"); err != nil {
		t.Fatal(err)
	}
	if got, err := cache.Ref(ctx, "refs/bundle/candidate"); err != nil || got != next {
		t.Fatalf("bundle ref = %q, %v; want %s", got, err, next)
	}
}

func TestCreateBundleRefusesCommitAlreadyInTheBase(t *testing.T) {
	ctx := context.Background()
	_, agent, base, tip := lineAndAgent(t, ctx)
	path := filepath.Join(t.TempDir(), "candidate.bundle")
	for _, c := range []struct{ commit, base string }{{base, base}, {base, tip}} {
		if err := agent.CreateBundle(ctx, path, "refs/sweatshop/candidate", c.commit, c.base); !errors.Is(err, ErrNothingToBundle) {
			t.Fatalf("CreateBundle(%s over %s) = %v, want ErrNothingToBundle", c.commit, c.base, err)
		}
	}
}

func TestCommitsSinceExcludesEverythingReachableFromBase(t *testing.T) {
	r, ctx := newRepo(t)
	base := commitFile(t, r, ctx, "a.txt", "1")
	mustGit(t, ctx, r.Dir, "checkout", "--quiet", "-b", "side")
	side := commitFile(t, r, ctx, "side.txt", "s")
	mustGit(t, ctx, r.Dir, "checkout", "--quiet", "master")
	main := commitFile(t, r, ctx, "main.txt", "m")
	mustGit(t, ctx, r.Dir, "merge", "--quiet", "--no-ff", "-m", "merge", "side")
	merge, _ := r.Commit(ctx, "HEAD")

	got, err := r.CommitsSince(ctx, base, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{side, main, merge}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("CommitsSince = %v, want %v", got, want)
	}
	if ok, err := r.IsAncestor(ctx, side, "HEAD"); err != nil || !ok {
		t.Fatalf("IsAncestor(side, merge) = %v, %v", ok, err)
	}
	if ok, err := r.IsAncestor(ctx, main, side); err != nil || ok {
		t.Fatalf("IsAncestor(main, side) = %v, %v", ok, err)
	}
	history, err := r.History(ctx, "HEAD")
	if err != nil || len(history) != 4 || history[0].Commit != merge || !strings.HasPrefix(history[0].Subject, "merge") {
		t.Fatalf("History = %+v, %v", history, err)
	}
}

func TestTrailerFollowsGitsTrailerBlockRules(t *testing.T) {
	r, ctx := newRepo(t)
	for _, c := range []struct {
		name, message string
		want          []string
	}{
		{"last paragraph", "fix\n\nbody text\n\nBead: a-1\nbead: a-2\nOther: x\n", []string{"a-1", "a-2"}},
		{"case-insensitive key", "fix\n\nBEAD: a-1\n", []string{"a-1"}},
		{"continuation line", "fix\n\nBead: a-1\n  more\n", []string{"a-1\nmore"}},
		{"subject only is not a trailer block", "Bead: a-1\n", nil},
		{"prose after trailers hides them", "fix\n\nBead: a-1\n\nthanks\n", nil},
		{"mixed paragraph is prose", "fix\n\nBead: a-1\nnot a trailer\n", nil},
	} {
		blob := commitFileWithMessage(t, r, ctx, c.name, c.message)
		got, err := r.Trailer(ctx, blob, "Bead")
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: Trailer = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

func commitFileWithMessage(t *testing.T, r Repo, ctx context.Context, name, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.Dir, "f.txt"), []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, ctx, r.Dir, "add", "-A")
	mustGit(t, ctx, r.Dir, "commit", "--quiet", "-m", message)
	sha, err := r.Commit(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

func TestPruneUnreachableDeletesOnlyUnreferencedObjectsPastTheGrace(t *testing.T) {
	r, ctx := newRepo(t)
	kept := commitFile(t, r, ctx, "a.txt", "1")
	loose, err := r.WriteBlob(ctx, []byte("unreferenced"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PruneUnreachable(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ObjectType(ctx, loose); err != nil {
		t.Fatalf("object inside the grace period was pruned: %v", err)
	}
	if err := r.PruneUnreachable(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ObjectType(ctx, loose); err == nil {
		t.Fatal("unreferenced object survived pruning")
	}
	if _, err := r.ObjectType(ctx, kept); err != nil {
		t.Fatalf("reachable commit was pruned: %v", err)
	}
}
