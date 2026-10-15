package witness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx/gittest"
)

func TestAttestDoesNotRunPromisorUploadpackFromAgentConfig(t *testing.T) {
	s := newSource(t)
	w, _ := newWitness(t)
	marker := filepath.Join(t.TempDir(), "pwned")
	for _, kv := range [][2]string{
		{"core.repositoryformatversion", "1"},
		{"extensions.partialClone", "evil"},
		{"remote.evil.url", t.TempDir()},
		{"remote.evil.uploadpack", "touch " + marker},
		{"remote.evil.promisor", "true"},
	} {
		ok(t)(gittest.Git(s.ctx, s.repo.Dir, "config", kv[0], kv[1]))
	}
	s.commit("src/a.go", "package a")
	if _, err := w.Attest(s.ctx, s.repo.Dir, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("witness executed uploadpack command from the agent's config")
	}
}

func TestAttestRejectsCommitNoLongerReachable(t *testing.T) {
	s := newSource(t)
	w, _ := newWitness(t)
	sha := s.commit("src/a.go", "package a")
	if _, err := w.Attest(s.ctx, s.repo.Dir, sha); err != nil {
		t.Fatal(err)
	}
	ok(t)(gittest.Git(s.ctx, s.repo.Dir, "reset", "--quiet", "--hard", "HEAD~1"))
	if _, err := w.Attest(s.ctx, s.repo.Dir, sha); err == nil {
		t.Fatal("attested a commit no agent branch contains, served from the cache")
	}
}

func TestAttestRefusesZeroChecks(t *testing.T) {
	s := newSource(t)
	w, _ := newWitness(t)
	w.Constitution.Checks = constitution.Checks{}
	if _, err := w.Attest(s.ctx, s.repo.Dir, "HEAD"); err == nil {
		t.Fatal("signed a run with zero checks")
	}
}

func TestConcurrentAttestsOfSameCommit(t *testing.T) {
	s := newSource(t)
	w, _ := newWitness(t)
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			r, err := w.Attest(s.ctx, s.repo.Dir, "HEAD")
			if err == nil && !r.Passed {
				err = os.ErrInvalid
			}
			errs <- err
		}()
	}
	for i := 0; i < 4; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
