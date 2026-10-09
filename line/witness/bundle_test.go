package witness

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
)

type bundleRig struct {
	t        *testing.T
	ctx      context.Context
	dir      string
	upstream gitx.Repo
	agent    gitx.Repo
	w        *Witness
	pub      ed25519.PublicKey
}

func newBundleRig(t *testing.T) *bundleRig {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	r := &bundleRig{t: t, ctx: ctx, dir: dir,
		upstream: gitx.Repo{Dir: filepath.Join(dir, "upstream.git")},
		agent:    gitx.Repo{Dir: filepath.Join(dir, "agent")}}
	root := gitx.Repo{Dir: dir}
	ok(t)(root.Git(ctx, "init", "--quiet", "--bare", "--initial-branch=master", r.upstream.Dir))
	ok(t)(root.Git(ctx, "clone", "--quiet", r.upstream.Dir, r.agent.Dir))
	ok(t)(r.agent.Git(ctx, "symbolic-ref", "HEAD", "refs/heads/master"))
	ok(t)(r.agent.Git(ctx, "config", "user.name", "t"))
	ok(t)(r.agent.Git(ctx, "config", "user.email", "t@example.com"))
	r.commit("check.sh", "test ! -e BROKEN\n")
	ok(t)(r.agent.Git(ctx, "push", "--quiet", "origin", "HEAD:refs/heads/master"))
	ok(t)(r.agent.Git(ctx, "fetch", "--quiet", "origin"))
	r.w, r.pub = newWitness(t)
	r.w.Upstream = r.upstream.Dir
	return r
}

func (r *bundleRig) commit(name, content string) string {
	r.t.Helper()
	full := filepath.Join(r.agent.Dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
	ok(r.t)(r.agent.Git(r.ctx, "add", "-A"))
	ok(r.t)(r.agent.Git(r.ctx, "commit", "--quiet", "-m", "change "+name))
	sha, err := r.agent.Commit(r.ctx, "HEAD")
	if err != nil {
		r.t.Fatal(err)
	}
	return sha
}

// bundle builds a candidate bundle the way the station client does: one ref
// under refs/sweatshop/, excluding what the line already has.
func (r *bundleRig) bundle(commit string) string {
	r.t.Helper()
	ok(r.t)(r.agent.Git(r.ctx, "update-ref", "refs/sweatshop/b", commit))
	f := filepath.Join(r.dir, fmt.Sprintf("b-%s.bundle", commit[:8]))
	ok(r.t)(r.agent.Git(r.ctx, "bundle", "create", "--quiet", f, "refs/sweatshop/b", "^origin/master"))
	return f
}

func TestAttestBundlePasses(t *testing.T) {
	r := newBundleRig(t)
	head := r.commit("src/a.go", "package a")
	res, err := r.w.AttestBundle(r.ctx, r.bundle(head), head)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed || res.Envelope == nil {
		t.Fatalf("result = %+v", res)
	}
	st, _, err := attest.VerifyStatement(*res.Envelope, map[string]ed25519.PublicKey{attest.KeyID(r.pub): r.pub})
	if err != nil {
		t.Fatal(err)
	}
	if c, err := st.Commit(); err != nil || c != head {
		t.Fatalf("statement commit %s, want %s", c, head)
	}
}

func TestAttestBundleRejectsMissingPrerequisites(t *testing.T) {
	r := newBundleRig(t)
	local := r.commit("local.txt", "only here") // never pushed
	head := r.commit("src/a.go", "package a")
	f := filepath.Join(r.dir, "p.bundle")
	ok(t)(r.agent.Git(r.ctx, "update-ref", "refs/sweatshop/b", head))
	ok(t)(r.agent.Git(r.ctx, "bundle", "create", "--quiet", f, "refs/sweatshop/b", "^"+local))
	if _, err := r.w.AttestBundle(r.ctx, f, head); err == nil {
		t.Fatal("accepted a bundle whose prerequisites the line lacks")
	}
}

func TestAttestBundleRejectsCommitNotInBundle(t *testing.T) {
	r := newBundleRig(t)
	head := r.commit("src/a.go", "package a")
	f := r.bundle(head)
	later := r.commit("src/b.go", "package b")
	if _, err := r.w.AttestBundle(r.ctx, f, later); err == nil {
		t.Fatal("attested a commit that is not in the bundle")
	}
	if _, err := r.w.AttestBundle(r.ctx, f, "abc123"); err == nil {
		t.Fatal("accepted an abbreviated commit id")
	}
}

func (r *bundleRig) head() string {
	r.t.Helper()
	sha, err := r.agent.Commit(r.ctx, "HEAD")
	if err != nil {
		r.t.Fatal(err)
	}
	return sha
}

// The cache keeps objects from earlier attestations, so cache.Commit alone
// proves nothing; the --contains check is what ties a commit to its bundle.
func TestAttestBundleRejectsCachedCommitOutsideBundleRef(t *testing.T) {
	r := newBundleRig(t)
	x := r.commit("src/x.go", "package x")
	if _, err := r.w.AttestBundle(r.ctx, r.bundle(x), x); err != nil {
		t.Fatal(err)
	}
	// x is now in the witness cache. Build a sibling branch that excludes it.
	ok(t)(r.agent.Git(r.ctx, "reset", "--quiet", "--hard", "origin/master"))
	y := r.commit("src/y.go", "package y")
	if _, err := r.w.AttestBundle(r.ctx, r.bundle(y), x); err == nil {
		t.Fatal("attested a cached commit that is not reachable from the bundle's ref")
	}
}

func TestAttestBundleAcceptsAncestorOfBundleRef(t *testing.T) {
	r := newBundleRig(t)
	c1 := r.commit("src/1.go", "package a")
	c2 := r.commit("src/2.go", "package a")
	res, err := r.w.AttestBundle(r.ctx, r.bundle(c2), c1)
	if err != nil || res.Commit != c1 {
		t.Fatalf("AttestBundle(ancestor) = %+v, %v", res, err)
	}
}

func TestAttestBundleIgnoresAgentRepoConfig(t *testing.T) {
	r := newBundleRig(t)
	head := r.commit("src/a.go", "package a")
	f := r.bundle(head)
	marker := filepath.Join(os.TempDir(), fmt.Sprintf("pwned-%d", time.Now().UnixNano()))
	t.Cleanup(func() { os.Remove(marker) })
	// Set the hostile config only after our own git calls in the agent repo.
	ok(t)(r.agent.Git(r.ctx, "config", "core.fsmonitor", "touch "+marker))
	if _, err := r.w.AttestBundle(r.ctx, f, head); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("witness executed code from the agent's repository")
	}
}

func TestAttestLineRequiresLineHistory(t *testing.T) {
	r := newBundleRig(t)
	master := r.head()
	res, err := r.w.AttestLine(r.ctx, master)
	if err != nil || !res.Passed || res.Commit != master {
		t.Fatalf("AttestLine(master) = %+v, %v", res, err)
	}
	other := r.commit("x.txt", "unpushed")
	if _, err := r.w.AttestLine(r.ctx, other); err == nil {
		t.Fatal("attested a commit that is not on the line")
	}
}

func TestAttestRequiresUpstream(t *testing.T) {
	r := newBundleRig(t)
	r.w.Upstream = ""
	master := r.head()
	if _, err := r.w.AttestLine(r.ctx, master); err == nil {
		t.Fatal("attested without an upstream")
	}
}

func TestMergeBundleMerges(t *testing.T) {
	r := newBundleRig(t)
	if err := guard.InstallHooks(r.upstream.Dir); err != nil {
		t.Fatal(err)
	}
	c := constitution.Constitution{
		Trust:  constitution.Trust{Level: "none", Keys: []constitution.TrustedKey{{Name: "witness", PublicKey: attest.EncodePublicKey(r.pub)}}},
		Checks: r.w.Constitution.Checks,
		Runner: r.w.Constitution.Runner,
	}
	g := guard.Guard{Repo: r.upstream, Ref: "refs/heads/master", Constitution: c, ConstitutionDigest: r.w.ConstitutionDigest}
	head := r.commit("src/a.go", "package a")
	res, err := r.w.MergeBundle(r.ctx, r.bundle(head), head, g)
	if err != nil {
		t.Fatal(err)
	}
	if res.Guard.Status != guard.Merged {
		t.Fatalf("result = %+v", res)
	}
	got, err := r.upstream.Commit(r.ctx, "refs/heads/master")
	if err != nil {
		t.Fatal(err)
	}
	if got != head {
		t.Fatalf("upstream master = %s, want %s", got, head)
	}
}
