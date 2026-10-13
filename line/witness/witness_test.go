package witness

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/report"
)

type source struct {
	t    *testing.T
	ctx  context.Context
	repo gitx.Repo
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

func newSource(t *testing.T) *source {
	t.Helper()
	s := &source{t: t, ctx: context.Background(), repo: gitx.Repo{Dir: t.TempDir()}}
	ok(t)(s.repo.Git(s.ctx, "init", "--quiet", "--initial-branch=master"))
	ok(t)(s.repo.Git(s.ctx, "config", "user.name", "test"))
	ok(t)(s.repo.Git(s.ctx, "config", "user.email", "test@example.com"))
	s.commit("check.sh", "test ! -e BROKEN\n")
	return s
}

func (s *source) write(name, content string) {
	s.t.Helper()
	full := filepath.Join(s.repo.Dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *source) commit(name, content string) string {
	s.t.Helper()
	s.write(name, content)
	ok(s.t)(s.repo.Git(s.ctx, "add", "-A"))
	ok(s.t)(s.repo.Git(s.ctx, "commit", "--quiet", "-m", "change "+name))
	sha, err := s.repo.Commit(s.ctx, "HEAD")
	if err != nil {
		s.t.Fatal(err)
	}
	return sha
}

func newWitness(t *testing.T) (*Witness, ed25519.PublicKey) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &Witness{
		Key: key, Role: "maker", AgentID: "maker-1", ConstitutionDigest: "sha256:test",
		Constitution: constitution.Constitution{
			Trust:  constitution.Trust{Level: "none"},
			Checks: constitution.Checks{Required: []constitution.Check{{Name: "check", Command: "sh check.sh"}}},
			Runner: constitution.Runner{Kind: "local"},
		},
		Runner:   ExecRunner{},
		CacheDir: filepath.Join(t.TempDir(), "cache.git"),
	}, pub
}

func TestAttestSignsPassingCommit(t *testing.T) {
	s := newSource(t)
	w, pub := newWitness(t)
	head := s.commit("src/a.go", "package a")
	result, err := w.Attest(s.ctx, s.repo.Dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed || result.Commit != head || result.Envelope == nil {
		t.Fatalf("result = %+v", result)
	}
	statement, _, err := attest.VerifyStatement(*result.Envelope, map[string]ed25519.PublicKey{attest.KeyID(pub): pub})
	if err != nil {
		t.Fatal(err)
	}
	if commit, _ := statement.Commit(); commit != head {
		t.Fatalf("attested commit %s, want %s", commit, head)
	}
	tree, _ := statement.Tree()
	want, _ := s.repo.Tree(s.ctx, head)
	if tree != want {
		t.Fatalf("attested tree %s, commit tree %s", tree, want)
	}
	p := statement.Predicate
	if p.Role != "maker" || p.AgentID != "maker-1" || p.Constitution != "sha256:test" || p.Runner != "local" {
		t.Fatalf("predicate = %+v", p)
	}
	if len(p.Checks) != 1 || p.Checks[0].ExitCode != 0 || p.Checks[0].Command != "sh check.sh" {
		t.Fatalf("checks = %+v", p.Checks)
	}
}

// blockingRunner waits for its context, like a check that never finishes,
// then reports a failing result the way a careless runner might.
type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, _, _ string) (RunResult, error) {
	<-ctx.Done()
	return RunResult{ExitCode: 137}, nil
}

func TestAttestCheckTimeoutIsAnErrorNotASignedFailure(t *testing.T) {
	s := newSource(t)
	w, _ := newWitness(t)
	w.Constitution.Checks.Timeout = "100ms"
	w.Runner = blockingRunner{}
	start := time.Now()
	result, err := w.Attest(s.ctx, s.repo.Dir, "HEAD")
	if err == nil || result.Envelope != nil {
		t.Fatalf("timed-out check produced %+v, %v; want an error and no signature", result, err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("timeout not applied: took %s", time.Since(start))
	}
}

func TestAttestSignsFailingCheck(t *testing.T) {
	s := newSource(t)
	w, pub := newWitness(t)
	s.commit("BROKEN", "x")
	result, err := w.Attest(s.ctx, s.repo.Dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed || result.Envelope == nil {
		t.Fatalf("failing run must be signed: %+v", result)
	}
	statement, _, err := attest.VerifyStatement(*result.Envelope, map[string]ed25519.PublicKey{attest.KeyID(pub): pub})
	if err != nil {
		t.Fatal(err)
	}
	if last := statement.Predicate.Checks[len(statement.Predicate.Checks)-1]; last.ExitCode == 0 {
		t.Fatalf("signed predicate hides the failure: %+v", statement.Predicate.Checks)
	}
}

func TestAttestIgnoresUncommittedWork(t *testing.T) {
	s := newSource(t)
	w, _ := newWitness(t)
	s.write("BROKEN", "untracked file that would fail the check in the agent's tree")
	result, err := w.Attest(s.ctx, s.repo.Dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed {
		t.Fatal("check saw the agent's working tree instead of a clean checkout")
	}
}

func TestAttestNeverRunsAgentHooksOrConfig(t *testing.T) {
	s := newSource(t)
	w, _ := newWitness(t)
	marker := filepath.Join(t.TempDir(), "pwned")
	hook := "#!/bin/sh\ntouch " + marker + "\n"
	for _, name := range []string{"post-checkout", "pre-push", "post-merge", "reference-transaction"} {
		if err := os.WriteFile(filepath.Join(s.repo.Dir, ".git", "hooks", name), []byte(hook), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ok(t)(s.repo.Git(s.ctx, "config", "core.fsmonitor", "touch "+marker))
	os.Remove(marker)
	if _, err := w.Attest(s.ctx, s.repo.Dir, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("witness executed code from the agent's repository")
	}
}

func TestAttestResolvesBranchesAndRejectsUnknownRevisions(t *testing.T) {
	s := newSource(t)
	w, _ := newWitness(t)
	head := s.commit("src/a.go", "package a")
	ok(t)(s.repo.Git(s.ctx, "branch", "feature"))
	s.commit("src/b.go", "package b")
	result, err := w.Attest(s.ctx, s.repo.Dir, "feature")
	if err != nil || result.Commit != head {
		t.Fatalf("Attest(feature) = %+v, %v", result, err)
	}
	if _, err := w.Attest(s.ctx, s.repo.Dir, "no-such-branch"); err == nil {
		t.Fatal("attested a revision that does not exist")
	}
	if _, err := w.Attest(s.ctx, s.repo.Dir, "--upload-pack=touch /tmp/x"); err == nil {
		t.Fatal("accepted an option-shaped revision")
	}
}

type reportObjects map[string][]byte

func (m reportObjects) Put(_ context.Context, key, _ string, data []byte) error {
	m[key] = data
	return nil
}

type reportRuns []report.Stats

func (m *reportRuns) RecordRun(_ context.Context, s report.Stats) error {
	*m = append(*m, s)
	return nil
}

func TestAttestPublishesReportAndStats(t *testing.T) {
	s := newSource(t)
	w, _ := newWitness(t)
	objs, runs := reportObjects{}, &reportRuns{}
	w.Reporter = &report.Reporter{Objects: objs, Runs: runs}
	head := s.commit("src/a.go", "package a")
	if _, err := w.Attest(s.ctx, s.repo.Dir, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if len(*runs) != 1 || (*runs)[0].Commit != head || !(*runs)[0].Passed || (*runs)[0].Checks != 1 {
		t.Fatalf("runs = %+v", *runs)
	}
	if len(objs) != 2 {
		t.Fatalf("objects = %d, want junit + html", len(objs))
	}
}

func TestAttestSweepsRefsLeftByEarlierProcess(t *testing.T) {
	s := newSource(t)
	w, _ := newWitness(t)
	head := s.commit("src/a.go", "package a")
	if _, err := w.Attest(s.ctx, s.repo.Dir, "HEAD"); err != nil {
		t.Fatal(err)
	}
	cache := gitx.Repo{Dir: w.CacheDir}
	stale := []string{"refs/bundle/deadbeef/x", "refs/witness/" + head + "/deadbeef"}
	for _, ref := range stale {
		ok(t)(cache.Git(s.ctx, "update-ref", ref, head))
	}
	restarted, _ := newWitness(t)
	restarted.CacheDir = w.CacheDir
	if _, err := restarted.Attest(s.ctx, s.repo.Dir, "HEAD"); err != nil {
		t.Fatal(err)
	}
	for _, ref := range stale {
		if exists, _ := cache.RefExists(s.ctx, ref); exists {
			t.Errorf("stale ref %s survived", ref)
		}
	}
}
