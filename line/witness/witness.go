// Package witness attests commits: it runs the constitution's required checks
// on a clean checkout it owns and signs the result. Agents never touch the
// checkout, the key, or the cache.
package witness

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
)

type Witness struct {
	Key                ed25519.PrivateKey
	Role               string
	AgentID            string
	Constitution       constitution.Constitution
	ConstitutionDigest string
	Runner             Runner
	// CacheDir is a witness-owned bare repository. The agent's repository is
	// only ever read through git fetch; git never runs inside it, so its
	// hooks and config cannot execute.
	CacheDir string
	Now      func() time.Time

	mu sync.Mutex
}

type AttestResult struct {
	Commit    string           `json:"commit"`
	Passed    bool             `json:"passed"`
	Statement attest.Statement `json:"statement"`
	Envelope  *attest.Envelope `json:"envelope,omitempty"`
}

func (w *Witness) Attest(ctx context.Context, sourceDir, rev string) (AttestResult, error) {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return AttestResult{}, fmt.Errorf("invalid revision %q", rev)
	}
	commit, tree, checkout, cleanup, err := w.prepare(ctx, sourceDir, rev)
	if err != nil {
		return AttestResult{}, err
	}
	defer cleanup()

	predicate := attest.ChecksPredicate{
		Commit: commit, Role: w.Role, AgentID: w.AgentID,
		Constitution: w.ConstitutionDigest, Runner: w.Constitution.Runner.ID(),
	}
	passed := true
	for _, check := range w.Constitution.Checks.Required {
		run, err := w.Runner.Run(ctx, checkout, check.Command)
		if err != nil {
			return AttestResult{}, fmt.Errorf("run check %s: %w", check.Name, err)
		}
		predicate.Checks = append(predicate.Checks, attest.CheckResult{
			Name: check.Name, Command: check.Command, ExitCode: run.ExitCode, EvidenceID: run.EvidenceID,
		})
		if run.ExitCode != 0 {
			passed = false
			break
		}
	}
	predicate.FinishedAt = w.now().UTC()

	statement := attest.NewStatement(tree, predicate)
	result := AttestResult{Commit: commit, Passed: passed, Statement: statement}
	if passed {
		env, err := attest.SignStatement(w.Key, statement)
		if err != nil {
			return AttestResult{}, err
		}
		result.Envelope = &env
	}
	return result, nil
}

// prepare serializes access to the shared cache: fetch, resolve, and check
// out under the lock; checks themselves run outside it.
func (w *Witness) prepare(ctx context.Context, sourceDir, rev string) (commit, tree, checkout string, cleanup func(), err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	cache, err := w.cache(ctx)
	if err != nil {
		return "", "", "", nil, err
	}
	if commit, err = w.fetch(ctx, cache, sourceDir, rev); err != nil {
		return "", "", "", nil, err
	}
	if tree, err = cache.Tree(ctx, commit); err != nil {
		return "", "", "", nil, err
	}
	if checkout, cleanup, err = w.checkout(ctx, cache, commit); err != nil {
		return "", "", "", nil, err
	}
	return commit, tree, checkout, cleanup, nil
}

func (w *Witness) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Witness) cache(ctx context.Context) (gitx.Repo, error) {
	repo := gitx.Repo{Dir: w.CacheDir}
	if _, err := os.Stat(filepath.Join(w.CacheDir, "HEAD")); err == nil {
		return repo, nil
	}
	if err := os.MkdirAll(w.CacheDir, 0o700); err != nil {
		return repo, err
	}
	_, err := repo.Git(ctx, "init", "--quiet", "--bare")
	return repo, err
}

func (w *Witness) fetch(ctx context.Context, cache gitx.Repo, sourceDir, rev string) (string, error) {
	_, err := cache.Git(ctx, "-c", "safe.directory="+sourceDir, "fetch", "--quiet", "--no-tags", "--force", "--end-of-options",
		sourceDir, "+HEAD:refs/source/HEAD", "+refs/heads/*:refs/source/heads/*")
	if err != nil {
		return "", fmt.Errorf("fetch from %s: %w", sourceDir, err)
	}
	for _, candidate := range []string{"refs/source/" + rev, "refs/source/heads/" + rev, rev} {
		if commit, err := cache.Commit(ctx, candidate); err == nil {
			return commit, nil
		}
	}
	return "", errors.New(rev + " is not a commit reachable from the agent's branches")
}

// checkout produces a self-contained repository at commit, so it also works
// when mounted into a container where the cache path does not exist.
func (w *Witness) checkout(ctx context.Context, cache gitx.Repo, commit string) (string, func(), error) {
	ref := "refs/witness/" + commit
	if _, err := cache.Git(ctx, "update-ref", ref, commit); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "witness-checkout-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() {
		_ = os.RemoveAll(dir)
		_ = cache.DeleteRef(context.WithoutCancel(ctx), ref)
	}
	work := gitx.Repo{Dir: dir}
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"fetch", "--quiet", "--no-tags", cache.Dir, ref},
		{"checkout", "--quiet", "--detach", "FETCH_HEAD"},
	} {
		if _, err := work.Git(ctx, args...); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return dir, cleanup, nil
}
