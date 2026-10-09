// Package harness drives the merge path end to end with scripted agents: no
// LLM, real git, real witness, real guard. Each test is one line scenario.
package harness

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/witness"
)

type line struct {
	t            *testing.T
	ctx          context.Context
	dir          string
	remote       gitx.Repo
	key          ed25519.PrivateKey
	constitution constitution.Constitution
	digest       string
	guard        guard.Guard
}

type agent struct {
	line    *line
	repo    gitx.Repo
	witness *witness.Witness
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

func newLine(t *testing.T) *line {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	l := &line{t: t, ctx: ctx, dir: dir, remote: gitx.Repo{Dir: filepath.Join(dir, "remote.git")}}
	root := gitx.Repo{Dir: dir}
	ok(t)(root.Git(ctx, "init", "--quiet", "--bare", "--initial-branch=master", l.remote.Dir))

	seed := gitx.Repo{Dir: filepath.Join(dir, "seed")}
	ok(t)(root.Git(ctx, "clone", "--quiet", l.remote.Dir, seed.Dir))
	ok(t)(seed.Git(ctx, "symbolic-ref", "HEAD", "refs/heads/master"))
	ok(t)(seed.Git(ctx, "-c", "user.name=seed", "-c", "user.email=seed@example.com", "commit", "--quiet", "--allow-empty", "-m", "seed"))
	writeFile(t, seed.Dir, "check.sh", "test ! -e BROKEN\n")
	ok(t)(seed.Git(ctx, "add", "-A"))
	ok(t)(seed.Git(ctx, "-c", "user.name=seed", "-c", "user.email=seed@example.com", "commit", "--quiet", "-m", "add check"))
	ok(t)(seed.Git(ctx, "push", "--quiet", "origin", "HEAD:refs/heads/master"))
	if err := guard.InstallHooks(l.remote.Dir); err != nil {
		t.Fatal(err)
	}

	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	l.key = key
	path := filepath.Join(dir, "line.toml")
	writeFile(t, dir, "line.toml", fmt.Sprintf(`[trust]
level = "none"

[[trust.keys]]
name = "witness"
public_key = %q

[runner]
kind = "local"

[[checks.required]]
name = "check"
command = "sh check.sh"

[[guards.paths]]
pattern = "acceptance/**"
roles = ["gemba"]
`, attest.EncodePublicKey(pub)))
	l.constitution, l.digest, err = constitution.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	l.guard = guard.Guard{Repo: l.remote, Ref: "refs/heads/master", Constitution: l.constitution, ConstitutionDigest: l.digest}
	return l
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (l *line) agent(name, role string) *agent {
	return l.agentWithKey(name, role, l.key)
}

func (l *line) agentWithKey(name, role string, key ed25519.PrivateKey) *agent {
	l.t.Helper()
	repo := gitx.Repo{Dir: filepath.Join(l.dir, "agents", name)}
	ok(l.t)((gitx.Repo{Dir: l.dir}).Git(l.ctx, "clone", "--quiet", l.remote.Dir, repo.Dir))
	ok(l.t)(repo.Git(l.ctx, "config", "user.name", name))
	ok(l.t)(repo.Git(l.ctx, "config", "user.email", name+"@example.com"))
	return &agent{line: l, repo: repo, witness: &witness.Witness{
		Key: key, Role: role, AgentID: name,
		Constitution: l.constitution, ConstitutionDigest: l.digest,
		Runner: witness.ExecRunner{}, CacheDir: filepath.Join(l.dir, "witness", name, "cache.git"),
	}}
}

func (l *line) master() string {
	l.t.Helper()
	sha, err := l.remote.Ref(l.ctx, "refs/heads/master")
	if err != nil {
		l.t.Fatal(err)
	}
	return sha
}

func (a *agent) change(name, content string) string {
	a.line.t.Helper()
	writeFile(a.line.t, a.repo.Dir, name, content)
	ok(a.line.t)(a.repo.Git(a.line.ctx, "add", "-A"))
	ok(a.line.t)(a.repo.Git(a.line.ctx, "commit", "--quiet", "-m", "change "+name))
	sha, err := a.repo.Commit(a.line.ctx, "HEAD")
	if err != nil {
		a.line.t.Fatal(err)
	}
	return sha
}

func (a *agent) merge() witness.MergeResult {
	a.line.t.Helper()
	result, err := a.witness.Merge(a.line.ctx, a.repo.Dir, "HEAD", a.line.remote.Dir, a.line.guard)
	if err != nil {
		a.line.t.Fatal(err)
	}
	return result
}

func (a *agent) rebase() {
	a.line.t.Helper()
	ok(a.line.t)(a.repo.Git(a.line.ctx, "fetch", "--quiet", "origin"))
	ok(a.line.t)(a.repo.Git(a.line.ctx, "rebase", "--quiet", "origin/master"))
}

func expect(t *testing.T, result witness.MergeResult, status guard.Status, reason guard.Reason) {
	t.Helper()
	if result.Guard.Status != status || result.Guard.Reason != reason {
		t.Fatalf("guard result = %+v, want %s/%s", result.Guard, status, reason)
	}
}

func TestMakerChangeMerges(t *testing.T) {
	l := newLine(t)
	maker := l.agent("maker-1", "maker")
	commit := maker.change("src/feature.go", "package src")
	expect(t, maker.merge(), guard.Merged, "")
	if l.master() != commit {
		t.Fatalf("master = %s, want %s", l.master(), commit)
	}
}

func TestFailingCheckNeverReachesTheGuard(t *testing.T) {
	l := newLine(t)
	before := l.master()
	maker := l.agent("maker-1", "maker")
	commit := maker.change("BROKEN", "this breaks the check")
	expect(t, maker.merge(), guard.Rejected, guard.ReasonChecksFailed)
	if l.master() != before {
		t.Fatal("master moved")
	}
	if ok, _ := l.remote.RefExists(l.ctx, guard.CandidatePrefix+commit); ok {
		t.Fatal("failing commit reached the remote")
	}
}

func TestLostRaceRebasesAndRetries(t *testing.T) {
	l := newLine(t)
	a := l.agent("maker-a", "maker")
	b := l.agent("maker-b", "maker")
	first := a.change("src/a.go", "package src // a")
	b.change("src/b.go", "package src // b")
	expect(t, a.merge(), guard.Merged, "")

	lost := b.merge()
	expect(t, lost, guard.Rejected, guard.ReasonBaseMoved)
	if lost.Guard.Base != first {
		t.Fatalf("base reported %s, want %s", lost.Guard.Base, first)
	}

	b.rebase()
	expect(t, b.merge(), guard.Merged, "")
	parents, err := l.remote.Parents(l.ctx, l.master())
	if err != nil || len(parents) != 1 || parents[0] != first {
		t.Fatalf("master's parent = %v, want %s (linear history)", parents, first)
	}
}

func TestForgedWitnessIsRejected(t *testing.T) {
	l := newLine(t)
	before := l.master()
	_, rogueKey, _ := ed25519.GenerateKey(rand.Reader)
	rogue := l.agentWithKey("rogue", "maker", rogueKey)
	rogue.change("src/backdoor.go", "package src")
	expect(t, rogue.merge(), guard.Rejected, guard.ReasonUntrusted)
	if l.master() != before {
		t.Fatal("master moved")
	}
}

func TestAttestationReplayedOnAnotherCommitIsRejected(t *testing.T) {
	l := newLine(t)
	maker := l.agent("maker-1", "maker")
	maker.change("src/honest.go", "package src")
	attested, err := maker.witness.Attest(l.ctx, maker.repo.Dir, "HEAD")
	if err != nil || !attested.Passed {
		t.Fatalf("attest: %+v %v", attested, err)
	}
	sneaky := maker.change("src/sneaky.go", "package src // never attested")
	ok(t)(maker.repo.Git(l.ctx, "push", "--quiet", "origin", sneaky+":"+guard.CandidatePrefix+sneaky))
	result, err := l.guard.Merge(l.ctx, sneaky, *attested.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != guard.Rejected || result.Reason != guard.ReasonTreeMismatch {
		t.Fatalf("result = %+v", result)
	}
}

func TestDirectPushToMasterIsRejected(t *testing.T) {
	l := newLine(t)
	maker := l.agent("maker-1", "maker")
	maker.change("src/a.go", "package src")
	_, err := maker.repo.Git(l.ctx, "push", "origin", "HEAD:master")
	if err == nil || !strings.Contains(err.Error(), "submit through the witness") {
		t.Fatalf("direct push: err = %v", err)
	}
}

func TestStoppedLineRejectsUntilCleared(t *testing.T) {
	l := newLine(t)
	ok(t)(l.remote.Git(l.ctx, "update-ref", guard.StopRef, l.master()))
	maker := l.agent("maker-1", "maker")
	commit := maker.change("src/a.go", "package src")
	expect(t, maker.merge(), guard.Rejected, guard.ReasonLineStopped)

	if err := l.remote.DeleteRef(l.ctx, guard.StopRef); err != nil {
		t.Fatal(err)
	}
	expect(t, maker.merge(), guard.Merged, "")
	if l.master() != commit {
		t.Fatal("merge after clearing the stop did not land")
	}
}

func TestOnlyGembaChangesAcceptanceTests(t *testing.T) {
	l := newLine(t)
	maker := l.agent("maker-1", "maker")
	maker.change("acceptance/login_test.sh", "exit 0")
	expect(t, maker.merge(), guard.Rejected, guard.ReasonRoleForbidden)

	gemba := l.agent("gemba-1", "gemba")
	commit := gemba.change("acceptance/login_test.sh", "test -f src/login.go")
	expect(t, gemba.merge(), guard.Merged, "")
	if l.master() != commit {
		t.Fatal("gemba change did not land")
	}
}
