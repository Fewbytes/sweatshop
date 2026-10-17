// Package witness attests commits: it runs the constitution's required checks
// on a clean checkout it owns and signs the result.
//
// In the M1 deployment the witness runs on the host inside `sweatshop line
// serve`, and the agent runs in a container that reaches it over the station
// API. Candidates arrive as git bundles (AttestBundle); the agent's repository
// and config are never read. Agent commands are not witnessed: trust rests on
// this package's clean-room check run.
//
// ExecRunner and Attest(sourceDir) run checks in the caller's uid and read a
// local directory; they are the trust-none development path (agentshd), where a
// malicious agent can read the key and alter witness state.
package witness

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/report"
)

type Witness struct {
	Key                ed25519.PrivateKey
	Role               string
	AgentID            string
	Constitution       constitution.Constitution
	ConstitutionDigest string
	Runner             Runner
	// CacheDir is a witness-owned bare repository. The agent's repository is
	// only ever read in process, for its refs and objects: no git program
	// runs against it and its config and hooks are never consulted.
	CacheDir string
	// Upstream is the path of the line's bare remote. It is trusted and the
	// witness only reads it, except to push candidates in MergeBundle.
	Upstream string
	Now      func() time.Time
	// Reporter, when set, publishes each completed run's JUnit/HTML reports
	// and records its duration and statistics. Failures are logged only: they
	// never change the verdict.
	Reporter *report.Reporter
	// Cold runs the checks with empty Go caches and network module lookups,
	// under the constitution's cold_timeout, to prove the line builds from
	// nothing. The signed predicate is the same as for a warm run.
	Cold bool

	mu    sync.Mutex
	swept bool
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
	if err := cache.Fetch(ctx, gitx.Repo{Dir: w.Upstream}, true, "+refs/heads/*:"+lineHeadsPrefix+"*"); err != nil {
		return fmt.Errorf("seed from upstream: %w", err)
	}
	return nil
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
		raw := make([]byte, 8)
		if _, err := rand.Read(raw); err != nil {
			return "", err
		}
		prefix := "refs/bundle/" + hex.EncodeToString(raw) + "/"
		defer w.deleteRefs(ctx, cache, prefix)
		if err := cache.FetchBundle(ctx, bundlePath, lineHeadsPrefix, false, "+refs/sweatshop/*:"+prefix+"*"); err != nil {
			return "", fmt.Errorf("fetch bundle: %w", err)
		}
		got, err := cache.Commit(ctx, commit)
		if err != nil || got != commit {
			return "", fmt.Errorf("%s is not in the bundle", commit)
		}
		in, err := cache.ReachableFromRefs(ctx, commit, prefix)
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
		in, err := cache.ReachableFromRefs(ctx, commit, lineHeadsPrefix)
		if err != nil {
			return "", err
		}
		if !in {
			return "", fmt.Errorf("%s is not reachable from the line's branches", commit)
		}
		return commit, nil
	})
}

// sweepStaleRefs removes per-attestation refs left behind by an earlier
// process that died before its deferred cleanup ran. It runs once per Witness,
// under w.mu, before this process creates any such ref.
func (w *Witness) sweepStaleRefs(ctx context.Context, cache gitx.Repo) {
	if w.swept {
		return
	}
	w.swept = true
	w.deleteRefs(ctx, cache, "refs/bundle/")
	w.deleteRefs(ctx, cache, "refs/witness/")
}

func (w *Witness) deleteRefs(ctx context.Context, cache gitx.Repo, prefix string) {
	ctx = context.WithoutCancel(ctx)
	refs, err := cache.RefNames(ctx, prefix)
	if err != nil {
		return
	}
	for _, ref := range refs {
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

// unreferencedGrace is how long an object no ref reaches stays in the cache,
// so an attestation in flight never loses objects it has just stored.
const unreferencedGrace = 14 * 24 * time.Hour

// lineHeadsPrefix is where seed mirrors the upstream's branches in the cache.
const lineHeadsPrefix = "refs/line/heads/"

var errCheckTimeout = errors.New("check timed out")

// checkEnv is prepended to every check command. It rides on the command
// because the check definition (`just ci`) is the candidate's and gated; the
// signed predicate keeps the constitution's command unchanged.
//   - -json makes every `go test` emit the JSON events that
//     report.ParseGoTestJSON reads; commands that do not accept it ignore it.
//   - -mod=readonly and GOPROXY=off make a module missing from the cache fail
//     at once with a clear error instead of a network lookup, so a DNS blip
//     cannot fail a check.
const checkEnv = `export GOFLAGS="${GOFLAGS:+$GOFLAGS }-json -mod=readonly" GOPROXY=off; `

// coldCheckEnv replaces checkEnv in a cold run: fresh throwaway caches (the
// volumes stay untouched) and the module proxy instead of GOPROXY=off.
const coldCheckEnv = `export GOFLAGS="${GOFLAGS:+$GOFLAGS }-json -mod=readonly" GOMODCACHE="$(mktemp -d)" GOCACHE="$(mktemp -d)" GOPROXY=https://proxy.golang.org,direct; `

// run executes the required checks in checkout and signs the outcome.
func (w *Witness) run(ctx context.Context, commit, tree, checkout string) (AttestResult, error) {
	predicate := attest.ChecksPredicate{
		Role: w.Role, AgentID: w.AgentID,
		Constitution: w.ConstitutionDigest, Runner: w.Constitution.Runner.ID(),
	}
	passed := true
	started := w.now()
	var timings []report.Check
	timeout, prefix := w.Constitution.Checks.TimeoutDuration(), checkEnv
	if w.Cold {
		timeout, prefix = w.Constitution.Checks.ColdTimeoutDuration(), coldCheckEnv
	}
	for _, check := range w.Constitution.Checks.Required {
		checkCtx, cancel := context.WithTimeoutCause(ctx, timeout, errCheckTimeout)
		checkStart := w.now()
		run, err := w.Runner.Run(checkCtx, checkout, prefix+check.Command)
		elapsed := w.now().Sub(checkStart)
		cut := checkCtx.Err() != nil
		cancel()
		// A check that timed out or was cancelled is a runner error, never a
		// signed failure: otherwise starving a check would let an agent mint
		// signed evidence that master fails (and pull the cord at will). The
		// run is still reported, as infra, so it shows in the CI metrics
		// without counting against any test.
		switch {
		case cut && ctx.Err() == nil:
			err = fmt.Errorf("run check %s: timed out after %s", check.Name, timeout)
		case cut:
			err = fmt.Errorf("run check %s: %w", check.Name, ctx.Err())
		case err != nil:
			err = fmt.Errorf("run check %s: %w", check.Name, err)
		}
		if err != nil {
			w.publish(ctx, report.Run{
				Commit: commit, Tree: tree, Role: w.Role, AgentID: w.AgentID, Runner: predicate.Runner,
				StartedAt: started, FinishedAt: w.now().UTC(), Checks: timings, Infra: err.Error(),
			})
			return AttestResult{}, err
		}
		predicate.Checks = append(predicate.Checks, attest.CheckResult{
			Name: check.Name, Command: check.Command, ExitCode: run.ExitCode, EvidenceID: run.EvidenceID,
		})
		timings = append(timings, report.Check{
			Name: check.Name, Command: check.Command, ExitCode: run.ExitCode,
			Duration: elapsed, EvidenceID: run.EvidenceID, Tests: report.ParseGoTestJSON(run.Output),
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
	w.publish(ctx, report.Run{
		Commit: commit, Tree: tree, Role: w.Role, AgentID: w.AgentID, Runner: predicate.Runner,
		StartedAt: started, FinishedAt: predicate.FinishedAt, Checks: timings,
	})
	return AttestResult{Commit: commit, Passed: passed, Statement: statement, Envelope: &env}, nil
}

func (w *Witness) publish(ctx context.Context, run report.Run) {
	if w.Reporter == nil {
		return
	}
	if err := w.Reporter.Publish(context.WithoutCancel(ctx), run); err != nil {
		log.Printf("witness: report %s: %v", run.Commit, err)
	}
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
	w.sweepStaleRefs(ctx, cache)
	// Rejected candidates and finished attestations leave unreferenced
	// objects; prune them once they pass the grace period.
	if err = cache.PruneUnreachable(ctx, unreferencedGrace); err != nil {
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
	return repo, repo.Init(ctx, true)
}

func (w *Witness) fetch(ctx context.Context, cache gitx.Repo, sourceDir, rev string) (string, error) {
	err := cache.Fetch(ctx, gitx.Repo{Dir: sourceDir}, true, "+HEAD:refs/source/HEAD", "+refs/heads/*:refs/source/heads/*")
	if err != nil {
		return "", fmt.Errorf("fetch from %s: %w", sourceDir, err)
	}
	for _, candidate := range []string{"refs/source/" + rev, "refs/source/heads/" + rev, rev} {
		commit, err := cache.Commit(ctx, candidate)
		if err != nil {
			continue
		}
		in, err := cache.ReachableFromRefs(ctx, commit, "refs/source/")
		if err != nil {
			return "", err
		}
		if in {
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
	if err := cache.SetRef(ctx, ref, commit); err != nil {
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
	if err := (gitx.Repo{Dir: dir}).CheckoutDetached(ctx, cache, commit); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := makeCheckoutWorldWritable(dir); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

// makeCheckoutWorldWritable lets any uid read and write the checkout. A
// container runner bind-mounts it at /src and runs the check as the image's
// unprivileged user with every capability dropped; where the container shares
// the host's uids (rootful podman on Linux) that user cannot enter a
// root-owned 0700 directory. Docker Desktop and the podman machine on macOS
// remap ownership, which is why this only shows on Linux. The checkout is
// disposable and private to this attestation.
func makeCheckoutWorldWritable(dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		perm := info.Mode().Perm() | 0o666
		if d.IsDir() || info.Mode().Perm()&0o111 != 0 {
			perm |= 0o111
		}
		return os.Chmod(path, perm)
	})
}
