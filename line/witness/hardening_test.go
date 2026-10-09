package witness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Fewbytes/sweatshop/line/constitution"
)

func TestParseGitVersion(t *testing.T) {
	cases := []struct {
		out string
		ok  bool
	}{
		{"git version 2.54.0", true},
		{"git version 2.45.1", true},
		{"git version 2.45.1.windows.1", true},
		{"git version 3.0.0", true},
		{"git version 2.45.0", false},
		{"git version 2.39.5 (Apple Git-154)", false},
		{"git version 1.9.9", false},
	}
	for _, c := range cases {
		v, err := parseGitVersion(c.out)
		if err != nil {
			t.Fatalf("%q: %v", c.out, err)
		}
		if got := gitAtLeastMin(v); got != c.ok {
			t.Errorf("%q: at least min = %v, want %v", c.out, got, c.ok)
		}
	}
	if _, err := parseGitVersion("not git"); err == nil {
		t.Error("accepted garbage version output")
	}
}

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
		ok(t)(s.repo.Git(s.ctx, "config", kv[0], kv[1]))
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
	ok(t)(s.repo.Git(s.ctx, "reset", "--quiet", "--hard", "HEAD~1"))
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
