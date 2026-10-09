package witness

import (
	"context"
	"fmt"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
)

// Merger is the merge guard as the witness sees it.
type Merger interface {
	Merge(ctx context.Context, commit string, env attest.Envelope) (guard.Result, error)
}

type MergeResult struct {
	Attestation AttestResult `json:"attestation"`
	Guard       guard.Result `json:"guard"`
}

// Merge attests rev and, only if every check passed, pushes it to remote as a
// candidate and asks the guard to merge it.
func (w *Witness) Merge(ctx context.Context, sourceDir, rev, remote string, merger Merger) (MergeResult, error) {
	attestation, err := w.Attest(ctx, sourceDir, rev)
	if err != nil {
		return MergeResult{}, err
	}
	return w.pushAndMerge(ctx, attestation, remote, merger)
}

// MergeBundle is Merge for a candidate delivered as a git bundle; the
// candidate is pushed to the witness's upstream.
func (w *Witness) MergeBundle(ctx context.Context, bundlePath, commit string, merger Merger) (MergeResult, error) {
	attestation, err := w.AttestBundle(ctx, bundlePath, commit)
	if err != nil {
		return MergeResult{}, err
	}
	return w.pushAndMerge(ctx, attestation, w.Upstream, merger)
}

func (w *Witness) pushAndMerge(ctx context.Context, attestation AttestResult, remote string, merger Merger) (MergeResult, error) {
	result := MergeResult{Attestation: attestation}
	if !attestation.Passed {
		result.Guard = guard.Result{
			Status: guard.Rejected, Reason: guard.ReasonChecksFailed, Commit: attestation.Commit,
			Detail: "witness checks failed; nothing was submitted",
		}
		return result, nil
	}
	cache := gitx.Repo{Dir: w.CacheDir}
	if _, err := cache.Git(ctx, "push", "--quiet", remote, attestation.Commit+":"+guard.CandidatePrefix+attestation.Commit); err != nil {
		return result, fmt.Errorf("push candidate: %w", err)
	}
	var err error
	result.Guard, err = merger.Merge(ctx, attestation.Commit, *attestation.Envelope)
	return result, err
}
