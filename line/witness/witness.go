// Package witness attests commits: it runs the constitution's required checks
// on a clean checkout it owns and signs the result.
//
// Agents cannot touch the checkout, the key, or the cache only when the agent
// runs sandboxed apart from the witness (the spec's deployment precondition).
// agentsh v1 runs both as the same host user (trust level none), so a malicious
// agent can read the key and alter witness state.
package witness

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	// only ever read through git fetch, and git-upload-pack does run against
	// it and reads its config. Safety relies on upload-pack being hardened
	// for untrusted repositories (git >= 2.45.1, enforced before the first
	// fetch) and on nothing else running there.
	CacheDir string
	// Upstream is the path of the line's bare remote. It is trusted and the
	// witness only reads it, except to push candidates in MergeBundle.
	Upstream string
	Now      func() time.Time

	mu         sync.Mutex
	gitChecked bool
}

const minGit = "2.45.1"

// parseGitVersion extracts major.minor.patch from `git version` output.
func parseGitVersion(out string) ([3]int, error) {
	var v [3]int
	fields := strings.Fields(out)
	if len(fields) < 3 || fields[0] != "git" || fields[1] != "version" {
		return v, fmt.Errorf("unrecognized git version output %q", out)
	}
	parts := strings.Split(fields[2], ".")
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			if i < 2 {
				return v, fmt.Errorf("unrecognized git version output %q", out)
			}
			break
		}
		v[i] = n
	}
	return v, nil
}

func gitAtLeastMin(v [3]int) bool {
	want := [3]int{2, 45, 1}
	for i := range v {
		if v[i] != want[i] {
			return v[i] > want[i]
		}
	}
	return true
}

// checkGit verifies once per Witness that git is hardened for fetching from
// untrusted repositories. Callers hold w.mu.
func (w *Witness) checkGit(ctx context.Context, cache gitx.Repo) error {
	if w.gitChecked {
		return nil
	}
	out, err := cache.Git(ctx, "version")
	if err != nil {
		return err
	}
	v, err := parseGitVersion(out)
	if err != nil {
		return err
	}
	if !gitAtLeastMin(v) {
		return fmt.Errorf("witness requires git >= %s, have %s", minGit, strings.TrimSpace(out))
	}
	w.gitChecked = true
	return nil
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
	return w.attest(ctx, func(ctx context.Context, cache gitx.Repo) (string, error) {
		return w.fetch(ctx, cache, sourceDir, rev)
	})
}

var fullHex = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

func checkFullHex(commit string) error {
	if !fullHex.MatchString(commit) {
		return fmt.Errorf("commit %q is not a full lowercase hex object id", commit)
	}
	return nil
}

// seed refreshes the cache's view of the line from the trusted upstream.
func (w *Witness) seed(ctx context.Context, cache gitx.Repo) error {
	if w.Upstream == "" {
		return errors.New("witness has no upstream")
	}
	if _, err := cache.Git(ctx, "fetch", "--quiet", "--no-tags", "--force", "--prune", "--end-of-options",
		w.Upstream, "+refs/heads/*:refs/line/heads/*"); err != nil {
		return fmt.Errorf("seed from upstream: %w", err)
	}
	return nil
}

// contained reports whether commit is reachable from a ref under prefix.
func contained(ctx context.Context, cache gitx.Repo, commit, prefix string) (bool, error) {
	out, err := cache.Git(ctx, "for-each-ref", "--count=1", "--format=%(refname)", "--contains", commit, prefix)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// AttestBundle attests commit, which must arrive in the git bundle at
// bundlePath. The agent's repository is never read: the bundle is verified
// against the line's own history and fetched into a nonce-named namespace.
func (w *Witness) AttestBundle(ctx context.Context, bundlePath, commit string) (AttestResult, error) {
	if err := checkFullHex(commit); err != nil {
		return AttestResult{}, err
	}
	return w.attest(ctx, func(ctx context.Context, cache gitx.Repo) (string, error) {
		if err := w.seed(ctx, cache); err != nil {
			return "", err
		}
		if _, err := cache.Git(ctx, "bundle", "verify", "--quiet", "--end-of-options", bundlePath); err != nil {
			return "", fmt.Errorf("bundle does not apply to the line: %w", err)
		}
		raw := make([]byte, 8)
		if _, err := rand.Read(raw); err != nil {
			return "", err
		}
		prefix := "refs/bundle/" + hex.EncodeToString(raw) + "/"
		defer w.deleteRefs(ctx, cache, prefix)
		if _, err := cache.Git(ctx, "fetch", "--quiet", "--no-tags", "--force", "--end-of-options",
			bundlePath, "+refs/sweatshop/*:"+prefix+"*"); err != nil {
			return "", fmt.Errorf("fetch bundle: %w", err)
		}
		got, err := cache.Commit(ctx, commit)
		if err != nil || got != commit {
			return "", fmt.Errorf("%s is not in the bundle", commit)
		}
		in, err := contained(ctx, cache, commit, prefix)
		if err != nil {
			return "", err
		}
		if !in {
			return "", fmt.Errorf("%s is not reachable from the bundle's ref", commit)
		}
		return commit, nil
	})
}

// AttestLine attests a commit already on the line, without any bundle. The
// Andon watcher uses it to attest master.
func (w *Witness) AttestLine(ctx context.Context, commit string) (AttestResult, error) {
	if err := checkFullHex(commit); err != nil {
		return AttestResult{}, err
	}
	return w.attest(ctx, func(ctx context.Context, cache gitx.Repo) (string, error) {
		if err := w.seed(ctx, cache); err != nil {
			return "", err
		}
		got, err := cache.Commit(ctx, commit)
		if err != nil || got != commit {
			return "", fmt.Errorf("%s is not on the line", commit)
		}
		in, err := contained(ctx, cache, commit, "refs/line/heads/")
		if err != nil {
			return "", err
		}
		if !in {
			return "", fmt.Errorf("%s is not reachable from the line's branches", commit)
		}
		return commit, nil
	})
}

func (w *Witness) deleteRefs(ctx context.Context, cache gitx.Repo, prefix string) {
	ctx = context.WithoutCancel(ctx)
	out, err := cache.Git(ctx, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		return
	}
	for _, ref := range strings.Fields(out) {
		_ = cache.DeleteRef(ctx, ref)
	}
}

// attest fetches via fetch, checks out the commit it returns, and runs and
// signs the required checks.
func (w *Witness) attest(ctx context.Context, fetch func(context.Context, gitx.Repo) (string, error)) (AttestResult, error) {
	if len(w.Constitution.Checks.Required) == 0 {
		return AttestResult{}, errors.New("constitution has no required checks; refusing to attest")
	}
	commit, tree, checkout, cleanup, err := w.prepare(ctx, fetch)
	if err != nil {
		return AttestResult{}, err
	}
	defer cleanup()
	return w.run(ctx, commit, tree, checkout)
}

// run executes the required checks in checkout and signs the outcome.
func (w *Witness) run(ctx context.Context, commit, tree, checkout string) (AttestResult, error) {
	predicate := attest.ChecksPredicate{
		Role: w.Role, AgentID: w.AgentID,
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

	statement := attest.NewStatement(commit, tree, predicate)
	// Failing runs are signed too: andon needs signed evidence of failure. The
	// guard rejects a failing attestation for merge, so this widens nothing.
	env, err := attest.SignStatement(w.Key, statement)
	if err != nil {
		return AttestResult{}, err
	}
	return AttestResult{Commit: commit, Passed: passed, Statement: statement, Envelope: &env}, nil
}

// prepare serializes access to the shared cache: fetch, resolve, and check
// out under the lock; checks themselves run outside it.
func (w *Witness) prepare(ctx context.Context, fetch func(context.Context, gitx.Repo) (string, error)) (commit, tree, checkout string, cleanup func(), err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	cache, err := w.cache(ctx)
	if err != nil {
		return "", "", "", nil, err
	}
	if err = w.checkGit(ctx, cache); err != nil {
		return "", "", "", nil, err
	}
	if commit, err = fetch(ctx, cache); err != nil {
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
	_, err := cache.Git(ctx, "-c", "safe.directory="+sourceDir, "fetch", "--quiet", "--no-tags", "--force", "--prune", "--end-of-options",
		sourceDir, "+HEAD:refs/source/HEAD", "+refs/heads/*:refs/source/heads/*")
	if err != nil {
		return "", fmt.Errorf("fetch from %s: %w", sourceDir, err)
	}
	for _, candidate := range []string{"refs/source/" + rev, "refs/source/heads/" + rev, rev} {
		commit, err := cache.Commit(ctx, candidate)
		if err != nil {
			continue
		}
		out, err := cache.Git(ctx, "for-each-ref", "--count=1", "--format=%(refname)", "--contains", commit, "refs/source/")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(out) != "" {
			return commit, nil
		}
	}
	return "", errors.New(rev + " is not reachable from the agent's branches")
}

// checkout produces a self-contained repository at commit, so it also works
// when mounted into a container where the cache path does not exist.
func (w *Witness) checkout(ctx context.Context, cache gitx.Repo, commit string) (string, func(), error) {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "", nil, err
	}
	ref := "refs/witness/" + commit + "/" + hex.EncodeToString(nonce)
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
