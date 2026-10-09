package guard

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
)

const digest = "sha256:test"

type fixture struct {
	t      *testing.T
	ctx    context.Context
	remote gitx.Repo
	work   gitx.Repo
	key    ed25519.PrivateKey
	guard  Guard
}

// ok checks a git call: ok(t)(repo.Git(...)). Go only lets a multi-value call
// fill an argument list on its own, hence the curried form.
func ok(t *testing.T) func(string, error) {
	return func(_ string, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	remote := gitx.Repo{Dir: filepath.Join(dir, "remote.git")}
	work := gitx.Repo{Dir: filepath.Join(dir, "work")}
	root := gitx.Repo{Dir: dir}
	ok(t)(root.Git(ctx, "init", "--quiet", "--bare", "--initial-branch=master", remote.Dir))
	ok(t)(root.Git(ctx, "clone", "--quiet", remote.Dir, work.Dir))
	ok(t)(work.Git(ctx, "symbolic-ref", "HEAD", "refs/heads/master"))
	ok(t)(work.Git(ctx, "config", "user.name", "test"))
	ok(t)(work.Git(ctx, "config", "user.email", "test@example.com"))
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, ctx: ctx, remote: remote, work: work, key: key}
	seed := f.commit("README", "seed")
	ok(t)(work.Git(ctx, "push", "--quiet", "origin", seed+":refs/heads/master"))
	f.guard = Guard{
		Repo: remote, Ref: "refs/heads/master", ConstitutionDigest: digest,
		Constitution: constitution.Constitution{
			Trust:  constitution.Trust{Level: "none", Keys: []constitution.TrustedKey{{Name: "w", PublicKey: attest.EncodePublicKey(pub)}}},
			Checks: constitution.Checks{Required: []constitution.Check{{Name: "ci", Command: "sh check.sh"}}},
			Runner: constitution.Runner{Kind: "local"},
			Guards: constitution.Guards{Paths: []constitution.PathGuard{{Pattern: "acceptance/**", Roles: []string{"gemba"}}}},
		},
	}
	return f
}

func (f *fixture) commit(name, content string) string {
	f.t.Helper()
	full := filepath.Join(f.work.Dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	ok(f.t)(f.work.Git(f.ctx, "add", "-A"))
	ok(f.t)(f.work.Git(f.ctx, "commit", "--quiet", "-m", "change "+name))
	sha, err := f.work.Commit(f.ctx, "HEAD")
	if err != nil {
		f.t.Fatal(err)
	}
	return sha
}

func (f *fixture) candidate(commit string) {
	f.t.Helper()
	ok(f.t)(f.work.Git(f.ctx, "push", "--quiet", "origin", commit+":"+CandidatePrefix+commit))
}

func (f *fixture) envelope(commit string, key ed25519.PrivateKey, mutate func(*attest.ChecksPredicate)) attest.Envelope {
	f.t.Helper()
	tree, err := f.work.Tree(f.ctx, commit)
	if err != nil {
		f.t.Fatal(err)
	}
	predicate := attest.ChecksPredicate{
		Commit: commit, Role: "maker", AgentID: "maker-1", Constitution: digest, Runner: "local",
		Checks:     []attest.CheckResult{{Name: "ci", Command: "sh check.sh", ExitCode: 0, EvidenceID: "inv_1"}},
		FinishedAt: time.Now().UTC(),
	}
	if mutate != nil {
		mutate(&predicate)
	}
	env, err := attest.SignStatement(key, attest.NewStatement(tree, predicate))
	if err != nil {
		f.t.Fatal(err)
	}
	return env
}

func (f *fixture) master() string {
	f.t.Helper()
	sha, err := f.remote.Ref(f.ctx, "refs/heads/master")
	if err != nil {
		f.t.Fatal(err)
	}
	return sha
}

func (f *fixture) expectRejected(commit string, env attest.Envelope, reason Reason) {
	f.t.Helper()
	before := f.master()
	result, err := f.guard.Merge(f.ctx, commit, env)
	if err != nil {
		f.t.Fatal(err)
	}
	if result.Status != Rejected || result.Reason != reason {
		f.t.Fatalf("result = %+v, want rejected/%s", result, reason)
	}
	if f.master() != before {
		f.t.Fatal("master moved on a rejected merge")
	}
}

func TestMergeFastForwardsMaster(t *testing.T) {
	f := newFixture(t)
	base := f.master()
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	result, err := f.guard.Merge(f.ctx, commit, f.envelope(commit, f.key, nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != Merged || result.Base != base || f.master() != commit {
		t.Fatalf("result = %+v, master = %s", result, f.master())
	}
	if ok, _ := f.remote.RefExists(f.ctx, CandidatePrefix+commit); ok {
		t.Fatal("candidate ref left behind after merge")
	}
}

func TestRejectsUntrustedKey(t *testing.T) {
	f := newFixture(t)
	_, attacker, _ := ed25519.GenerateKey(rand.Reader)
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	f.expectRejected(commit, f.envelope(commit, attacker, nil), ReasonUntrusted)
}

func TestRejectsAttestationForAnotherTree(t *testing.T) {
	f := newFixture(t)
	honest := f.commit("src/a.go", "package a")
	sneaky := f.commit("src/b.go", "package b // unattested")
	f.candidate(sneaky)
	f.expectRejected(sneaky, f.envelope(honest, f.key, nil), ReasonTreeMismatch)
}

func TestRejectsOtherConstitution(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	f.expectRejected(commit, f.envelope(commit, f.key, func(p *attest.ChecksPredicate) { p.Constitution = "sha256:old" }), ReasonConstitution)
}

func TestRejectsOtherRunner(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	f.expectRejected(commit, f.envelope(commit, f.key, func(p *attest.ChecksPredicate) { p.Runner = "container:docker:x" }), ReasonRunner)
}

func TestRejectsMissingCheck(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	f.expectRejected(commit, f.envelope(commit, f.key, func(p *attest.ChecksPredicate) { p.Checks = nil }), ReasonChecksMissing)
}

func TestRejectsCheckWithDifferentCommand(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	f.expectRejected(commit, f.envelope(commit, f.key, func(p *attest.ChecksPredicate) { p.Checks[0].Command = "true" }), ReasonChecksMissing)
}

func TestRejectsFailedCheckEvenWhenSigned(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	f.expectRejected(commit, f.envelope(commit, f.key, func(p *attest.ChecksPredicate) { p.Checks[0].ExitCode = 1 }), ReasonChecksFailed)
}

func TestRejectsWhenLineStopped(t *testing.T) {
	f := newFixture(t)
	ok(t)(f.remote.Git(f.ctx, "update-ref", StopRef, f.master()))
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	f.expectRejected(commit, f.envelope(commit, f.key, nil), ReasonLineStopped)
}

func TestRejectsStaleBase(t *testing.T) {
	f := newFixture(t)
	seed := f.master()
	first := f.commit("src/a.go", "package a")
	f.candidate(first)
	if r, err := f.guard.Merge(f.ctx, first, f.envelope(first, f.key, nil)); err != nil || r.Status != Merged {
		t.Fatalf("first merge: %+v %v", r, err)
	}
	ok(t)(f.work.Git(f.ctx, "reset", "--quiet", "--hard", seed))
	stale := f.commit("src/b.go", "package b")
	f.candidate(stale)
	result, err := f.guard.Merge(f.ctx, stale, f.envelope(stale, f.key, nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != Rejected || result.Reason != ReasonBaseMoved || result.Base != first {
		t.Fatalf("result = %+v", result)
	}
}

func TestRolesGatePaths(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("acceptance/login_test.go", "package acceptance")
	f.candidate(commit)
	f.expectRejected(commit, f.envelope(commit, f.key, nil), ReasonRoleForbidden)

	f.candidate(commit)
	result, err := f.guard.Merge(f.ctx, commit, f.envelope(commit, f.key, func(p *attest.ChecksPredicate) { p.Role = "gemba" }))
	if err != nil || result.Status != Merged {
		t.Fatalf("gemba merge: %+v %v", result, err)
	}
}

func TestRejectsRefNameCandidate(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	before := f.master()
	// Try to pass a ref name instead of a full commit ID
	result, err := f.guard.Merge(f.ctx, CandidatePrefix+commit, f.envelope(commit, f.key, nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != Rejected || result.Reason != ReasonBadCandidate {
		t.Fatalf("result = %+v, want rejected/bad_candidate", result)
	}
	if f.master() != before {
		t.Fatal("master moved on a rejected merge")
	}
}

func TestRejectsAbbreviatedSHA(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	before := f.master()
	// Try to pass abbreviated SHA
	result, err := f.guard.Merge(f.ctx, commit[:12], f.envelope(commit, f.key, nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != Rejected || result.Reason != ReasonBadCandidate {
		t.Fatalf("result = %+v, want rejected/bad_candidate", result)
	}
	if f.master() != before {
		t.Fatal("master moved on a rejected merge")
	}
}

func TestRejectsNonexistentCommit(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	before := f.master()
	// Use a valid-looking hex that doesn't exist
	fakeCommit := "0000000000000000000000000000000000000000"
	result, err := f.guard.Merge(f.ctx, fakeCommit, f.envelope(commit, f.key, nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != Rejected || result.Reason != ReasonBadCandidate {
		t.Fatalf("result = %+v, want rejected/bad_candidate", result)
	}
	if f.master() != before {
		t.Fatal("master moved on a rejected merge")
	}
}
