package witness

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
)

type fakeMerger struct {
	remote gitx.Repo
	called bool
	commit string
	env    attest.Envelope
}

func (f *fakeMerger) Merge(ctx context.Context, commit string, env attest.Envelope) (guard.Result, error) {
	f.called, f.commit, f.env = true, commit, env
	if ok, _ := f.remote.RefExists(ctx, guard.CandidatePrefix+commit); !ok {
		return guard.Result{Status: guard.Rejected, Reason: "no_candidate", Commit: commit}, nil
	}
	return guard.Result{Status: guard.Merged, Commit: commit}, nil
}

func newRemote(t *testing.T) gitx.Repo {
	t.Helper()
	remote := gitx.Repo{Dir: filepath.Join(t.TempDir(), "remote.git")}
	ok(t)((gitx.Repo{Dir: filepath.Dir(remote.Dir)}).Git(context.Background(), "init", "--quiet", "--bare", remote.Dir))
	return remote
}

func TestMergePushesCandidateAndCallsGuard(t *testing.T) {
	s := newSource(t)
	w, _ := newWitness(t)
	remote := newRemote(t)
	merger := &fakeMerger{remote: remote}
	head := s.commit("src/a.go", "package a")
	result, err := w.Merge(s.ctx, s.repo.Dir, "HEAD", remote.Dir, merger)
	if err != nil {
		t.Fatal(err)
	}
	if !merger.called || merger.commit != head || result.Guard.Status != guard.Merged {
		t.Fatalf("result = %+v, merger = %+v", result, merger)
	}
	if len(merger.env.Signatures) != 1 {
		t.Fatal("guard did not receive the signed envelope")
	}
}

func TestMergeWithFailingChecksNeverReachesGuard(t *testing.T) {
	s := newSource(t)
	w, _ := newWitness(t)
	remote := newRemote(t)
	merger := &fakeMerger{remote: remote}
	head := s.commit("BROKEN", "x")
	result, err := w.Merge(s.ctx, s.repo.Dir, "HEAD", remote.Dir, merger)
	if err != nil {
		t.Fatal(err)
	}
	if merger.called || result.Guard.Status != guard.Rejected || result.Guard.Reason != guard.ReasonChecksFailed {
		t.Fatalf("result = %+v, merger called = %v", result, merger.called)
	}
	if ok, _ := remote.RefExists(s.ctx, guard.CandidatePrefix+head); ok {
		t.Fatal("failed commit was pushed as a candidate")
	}
}
