// Package guard is the merge guard: the only writer of the guarded branch.
// It accepts a candidate commit only with a trusted attestation that matches
// the commit's tree, the current constitution, and the required checks, and
// only as a fast-forward of the branch, applied by compare-and-swap.
package guard

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
)

const (
	StopRef         = "refs/line/andon"
	CandidatePrefix = "refs/line/candidates/"
)

type Status string

const (
	Merged   Status = "merged"
	Rejected Status = "rejected"
)

type Reason string

const (
	ReasonUntrusted      Reason = "untrusted_attestation"
	ReasonCommitMismatch Reason = "commit_mismatch"
	ReasonConstitution   Reason = "constitution_mismatch"
	ReasonRunner         Reason = "runner_mismatch"
	ReasonChecksMissing  Reason = "checks_missing"
	ReasonChecksFailed   Reason = "checks_failed"
	ReasonLineStopped    Reason = "line_stopped"
	ReasonBaseMoved      Reason = "base_moved"
	ReasonRoleForbidden  Reason = "role_forbidden"
	ReasonBadCandidate   Reason = "bad_candidate"
	ReasonBlobForbidden  Reason = "blob_forbidden"

	ReasonRoleNotPermitted Reason = "role_not_permitted"
	ReasonNoBase           Reason = "no_base"
)

type Result struct {
	Status Status `json:"status"`
	Reason Reason `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
	Commit string `json:"commit"`
	Base   string `json:"base,omitempty"`
	Event  string `json:"event,omitempty"` // andon event blob ID
}

type Guard struct {
	Repo               gitx.Repo
	Ref                string
	Constitution       constitution.Constitution
	ConstitutionDigest string
	Now                func() time.Time // defaults to time.Now
}

func (g Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g Guard) Merge(ctx context.Context, commit string, env attest.Envelope) (Result, error) {
	reject := func(reason Reason, detail string) (Result, error) {
		return Result{Status: Rejected, Reason: reason, Detail: detail, Commit: commit}, nil
	}

	// Validate commit is a full lowercase hex of length 40 or 64
	if !isValidHexCommit(commit) {
		return reject(ReasonBadCandidate, fmt.Sprintf("candidate must be a full-length hex commit ID (40 or 64 chars), got %q", commit))
	}

	// Resolve commit to ensure it exists and is a commit object
	resolved, err := g.Repo.Commit(ctx, commit)
	if err != nil {
		return reject(ReasonBadCandidate, fmt.Sprintf("candidate commit not found: %v", err))
	}
	if resolved != commit {
		return reject(ReasonBadCandidate, fmt.Sprintf("resolved to different ID: input %s, resolved %s", commit, resolved))
	}

	// Now safe to defer cleanup using validated ID
	defer g.Repo.DeleteRef(context.WithoutCancel(ctx), CandidatePrefix+commit)

	v, rejected, err := g.verify(env, commit, commit)
	if err != nil {
		return Result{}, err
	}
	if rejected != nil {
		return *rejected, nil
	}
	predicate := v.statement.Predicate
	docsOnly := false
	if slices.ContainsFunc(predicate.Checks, func(r attest.CheckResult) bool { return r.Skipped != "" }) {
		paths, hasParent, err := g.Repo.FirstParentChange(ctx, commit)
		if err != nil {
			return Result{}, err
		}
		docsOnly = hasParent && g.Constitution.Checks.DocsOnly(paths)
	}
	if reason, detail := checkResults(g.Constitution.Checks.Required, predicate.Checks, docsOnly); reason != "" {
		return reject(reason, detail)
	}

	// While the cord is pulled only a fix tagged with the event may pass.
	stopID, err := g.stopID(ctx)
	if err != nil {
		return Result{}, err
	}
	if stopID != "" {
		tagged, err := g.Repo.Trailer(ctx, commit, FixTrailer)
		if err != nil {
			return Result{}, err
		}
		if !slices.Contains(tagged, stopID) {
			result, _ := reject(ReasonLineStopped, fmt.Sprintf("the andon cord is pulled (event %s); fixes must carry the trailer %s: %s", stopID, FixTrailer, stopID))
			result.Event = stopID
			return result, nil
		}
	}

	exists, err := g.Repo.RefExists(ctx, g.Ref)
	if err != nil {
		return Result{}, err
	}
	if !exists {
		return reject(ReasonNoBase, g.Ref+" does not exist; seed it before installing the guard's hook")
	}
	base, err := g.Repo.Ref(ctx, g.Ref)
	if err != nil {
		return Result{}, fmt.Errorf("read %s: %w", g.Ref, err)
	}
	parents, err := g.Repo.Parents(ctx, commit)
	if err != nil {
		return Result{}, err
	}
	if len(parents) == 0 || parents[0] != base {
		result, _ := reject(ReasonBaseMoved, "the candidate's first parent is not the current "+g.Ref+"; rebase and retry")
		result.Base = base
		return result, nil
	}

	paths, err := g.Repo.ChangedPaths(ctx, base, commit)
	if err != nil {
		return Result{}, err
	}
	for _, p := range paths {
		if roles, guarded := g.Constitution.AllowedRoles(p); guarded && !slices.Contains(roles, predicate.Role) {
			return reject(ReasonRoleForbidden, fmt.Sprintf("%s may only be changed by %v, not %s", p, roles, predicate.Role))
		}
	}

	blobs, err := g.Repo.ChangedBlobs(ctx, base, commit)
	if err != nil {
		return Result{}, err
	}
	if detail := blobViolation(g.Constitution.Guards.Blobs, blobs); detail != "" {
		return reject(ReasonBlobForbidden, detail)
	}

	if err := g.Repo.UpdateRef(ctx, g.Ref, commit, base); err != nil {
		if errors.Is(err, gitx.ErrRefMoved) {
			result, _ := reject(ReasonBaseMoved, g.Ref+" moved during the merge; rebase and retry")
			result.Base = base
			return result, nil
		}
		return Result{}, err
	}
	return Result{Status: Merged, Commit: commit, Base: base}, nil
}

var hexCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$|^[0-9a-f]{64}$`)

func isValidHexCommit(commit string) bool {
	return hexCommitPattern.MatchString(commit)
}

// checkResults accepts a skipped check only when the constitution allows
// skipping it and the guard itself found the commit docs-only; the witness's
// word is not enough.
func checkResults(required []constitution.Check, results []attest.CheckResult, docsOnly bool) (Reason, string) {
	byName := make(map[string]attest.CheckResult, len(results))
	for _, r := range results {
		byName[r.Name] = r
	}
	for _, check := range required {
		r, ok := byName[check.Name]
		if !ok || r.Command != check.Command {
			return ReasonChecksMissing, fmt.Sprintf("required check %q (%s) is not attested", check.Name, check.Command)
		}
		if r.Skipped != "" {
			if !check.SkipForDocsOnly || !docsOnly {
				return ReasonChecksMissing, fmt.Sprintf("required check %q was skipped (%s) but the commit is not a docs-only change the constitution lets it skip", check.Name, r.Skipped)
			}
			continue
		}
		if r.ExitCode != 0 {
			return ReasonChecksFailed, fmt.Sprintf("check %q exited %d", check.Name, r.ExitCode)
		}
	}
	return "", ""
}
