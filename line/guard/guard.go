// Package guard is the merge guard: the only writer of the guarded branch.
// It accepts a candidate commit only with a trusted attestation that matches
// the commit's tree, the current constitution, and the required checks, and
// only as a fast-forward of the branch, applied by compare-and-swap.
package guard

import (
	"context"
	"errors"
	"fmt"
	"slices"

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
	ReasonUntrusted     Reason = "untrusted_attestation"
	ReasonTreeMismatch  Reason = "tree_mismatch"
	ReasonConstitution  Reason = "constitution_mismatch"
	ReasonRunner        Reason = "runner_mismatch"
	ReasonChecksMissing Reason = "checks_missing"
	ReasonChecksFailed  Reason = "checks_failed"
	ReasonLineStopped   Reason = "line_stopped"
	ReasonBaseMoved     Reason = "base_moved"
	ReasonRoleForbidden Reason = "role_forbidden"
)

type Result struct {
	Status Status `json:"status"`
	Reason Reason `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
	Commit string `json:"commit"`
	Base   string `json:"base,omitempty"`
}

type Guard struct {
	Repo               gitx.Repo
	Ref                string
	Constitution       constitution.Constitution
	ConstitutionDigest string
}

func (g Guard) Merge(ctx context.Context, commit string, env attest.Envelope) (Result, error) {
	defer g.Repo.DeleteRef(context.WithoutCancel(ctx), CandidatePrefix+commit)
	reject := func(reason Reason, detail string) (Result, error) {
		return Result{Status: Rejected, Reason: reason, Detail: detail, Commit: commit}, nil
	}

	trusted, err := g.Constitution.TrustedKeys()
	if err != nil {
		return Result{}, err
	}
	statement, _, err := attest.VerifyStatement(env, trusted)
	if err != nil {
		return reject(ReasonUntrusted, err.Error())
	}
	attestedTree, err := statement.Tree()
	if err != nil {
		return reject(ReasonUntrusted, err.Error())
	}
	tree, err := g.Repo.Tree(ctx, commit)
	if err != nil {
		return Result{}, fmt.Errorf("resolve candidate %s: %w", commit, err)
	}
	if tree != attestedTree {
		return reject(ReasonTreeMismatch, fmt.Sprintf("commit tree %s, attested tree %s", tree, attestedTree))
	}

	predicate := statement.Predicate
	if predicate.Constitution != g.ConstitutionDigest {
		return reject(ReasonConstitution, fmt.Sprintf("attested under %s, line is at %s", predicate.Constitution, g.ConstitutionDigest))
	}
	if want := g.Constitution.Runner.ID(); predicate.Runner != want {
		return reject(ReasonRunner, fmt.Sprintf("checks ran on %s, constitution requires %s", predicate.Runner, want))
	}
	if reason, detail := checkResults(g.Constitution.Checks.Required, predicate.Checks); reason != "" {
		return reject(reason, detail)
	}

	stopped, err := g.Repo.RefExists(ctx, StopRef)
	if err != nil {
		return Result{}, err
	}
	if stopped {
		return reject(ReasonLineStopped, "the andon cord is pulled")
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

func checkResults(required []constitution.Check, results []attest.CheckResult) (Reason, string) {
	byName := make(map[string]attest.CheckResult, len(results))
	for _, r := range results {
		byName[r.Name] = r
	}
	for _, check := range required {
		r, ok := byName[check.Name]
		if !ok || r.Command != check.Command {
			return ReasonChecksMissing, fmt.Sprintf("required check %q (%s) is not attested", check.Name, check.Command)
		}
		if r.ExitCode != 0 {
			return ReasonChecksFailed, fmt.Sprintf("check %q exited %d", check.Name, r.ExitCode)
		}
	}
	return "", ""
}
