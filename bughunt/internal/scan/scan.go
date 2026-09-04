// Package scan runs the deterministic detectors and records what they find.
// It never calls an LLM: scan is the verb that gates commits, so it must be
// reproducible and bounded.
package scan

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/bughunt/internal/config"
	"github.com/Fewbytes/sweatshop/bughunt/internal/detector"
	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/gitinfo"
	"github.com/Fewbytes/sweatshop/bughunt/internal/store"
)

// Options configures one scan.
type Options struct {
	// Dir is the repository root.
	Dir string
	// CommitSHA is recorded on the run, for correlation.
	CommitSHA string
	// DiffBase is the revision --diff was given, empty in full mode.
	DiffBase string
	// ChangedLines maps repo-relative paths to changed line numbers. Only
	// consulted when DiffBase is set. Findings outside it are still recorded;
	// they simply do not gate.
	ChangedLines map[string][]int
	// Paths narrows the analysis, empty means the whole tree.
	Paths []string
}

// Recorded is one hit after fingerprinting and persistence.
type Recorded struct {
	Hit         finding.Hit
	Fingerprint string
	Status      store.Status
	// Gates is whether this finding contributes to a non-zero exit.
	Gates bool
}

// Result is the outcome of one scan.
type Result struct {
	RunID                string
	Findings             []Recorded
	DetectorsUsed        []string
	DetectorsUnavailable []string
	GatingCount          int
}

// ExitCode is 1 when anything gates, 0 otherwise.
func (r Result) ExitCode() int {
	if r.GatingCount > 0 {
		return 1
	}
	return 0
}

// Run executes every enabled and available detector, records the results, and
// reports what gates.
func Run(ctx context.Context, s *store.Store, reg *detector.Registry,
	cfg config.Config, opts Options) (Result, error) {

	runID, err := newRunID()
	if err != nil {
		return Result{}, err
	}
	res := Result{RunID: runID}

	enabled := reg.Enabled(cfg)
	available, unavailable := reg.Partition(ctx, enabled)
	for _, d := range available {
		res.DetectorsUsed = append(res.DetectorsUsed, d.Name())
	}
	for _, d := range unavailable {
		res.DetectorsUnavailable = append(res.DetectorsUnavailable, d.Name())
	}

	floor := finding.Severity(cfg.SeverityFloor)
	// Resolved once per run, not per hit. Empty when Dir is not inside a git
	// repository, which is a supported way to run bughunt.
	repoRoot := resolveRepoRoot(opts.Dir)
	// One fingerprint is recorded at most once per run. Detectors legitimately
	// report the same defect twice — two identical `defer rows.Close()` lines in
	// one function share a symbol and normalized match text — and recording both
	// would bump seen_count twice for a single sighting and overstate the counts.
	seen := map[string]bool{}
	for _, d := range available {
		hits, err := d.Run(ctx, opts.Dir, opts.Paths)
		if err != nil {
			return Result{}, fmt.Errorf("detector %s: %w", d.Name(), err)
		}
		for _, h := range hits {
			// Normalize before anything reads File. Excludes, diff matching, and
			// the fingerprint's fallback anchor all compare paths, and until this
			// runs they are comparing paths measured from different roots.
			h.File = normalizePath(h.File, opts.Dir, repoRoot)
			if !h.Severity.AtLeast(floor) || cfg.Excluded(h.File) {
				continue
			}
			fp := finding.Fingerprint(h)
			if seen[fp] {
				continue
			}
			seen[fp] = true
			rec, err := record(ctx, s, runID, h, opts)
			if err != nil {
				return Result{}, err
			}
			res.Findings = append(res.Findings, rec)
			if rec.Gates {
				res.GatingCount++
			}
		}
	}

	err = s.InsertRun(ctx, store.Run{
		ID:                   runID,
		StartedAt:            time.Now().UTC().Format(time.RFC3339),
		CommitSHA:            opts.CommitSHA,
		Mode:                 "scan",
		DiffBase:             opts.DiffBase,
		DetectorsUsed:        res.DetectorsUsed,
		DetectorsUnavailable: res.DetectorsUnavailable,
		FindingCount:         len(res.Findings),
		NewCount:             res.GatingCount,
	})
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

func record(ctx context.Context, s *store.Store, runID string,
	h finding.Hit, opts Options) (Recorded, error) {

	fp := finding.Fingerprint(h)
	status, err := s.UpsertFinding(ctx, store.Finding{
		Fingerprint:  fp,
		RuleID:       h.RuleID,
		Detector:     detectorOf(h.RuleID),
		File:         h.File,
		Line:         h.Line,
		Symbol:       h.Symbol,
		Message:      h.Message,
		Severity:     string(h.Severity),
		Confidence:   1,
		FirstSeenRun: runID,
		LastSeenRun:  runID,
	})
	if err != nil {
		return Recorded{}, err
	}
	return Recorded{
		Hit:         h,
		Fingerprint: fp,
		Status:      status,
		Gates:       status.Gates() && inDiff(h, opts),
	}, nil
}

// resolveRepoRoot returns the repository root containing dir, or "" when dir is
// not inside a repository. A missing repository is not an error: bughunt has to
// run outside one, it simply cannot offer diff mode there.
func resolveRepoRoot(dir string) string {
	root, err := gitinfo.RepoRoot(dir)
	if err != nil {
		return ""
	}
	return root
}

// normalizePath rewrites a detector's path to be relative to the repository
// root, which is the frame git reports changed files in.
//
// Detectors disagree: the Go line tools report paths relative to the directory
// they ran in, gosec reports them absolute. Left alone, a hit from a scan run
// inside agentsh/ reads "cmd/main.go" while git calls the same file
// "agentsh/cmd/main.go", so diff matching never fires and the gate silently
// passes. Excludes miss for the same reason.
//
// Outside a repository, paths are made relative to dir instead, which keeps
// them stable and machine-independent even though diff mode is unavailable.
func normalizePath(path, dir, repoRoot string) string {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(dir, path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}

	base := repoRoot
	if base == "" {
		base = dir
		if resolved, err := filepath.EvalSymlinks(base); err == nil {
			base = resolved
		}
	}

	rel, err := filepath.Rel(base, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		// Outside the base entirely — keep what the detector said rather than
		// inventing a path that escapes the tree.
		return path
	}
	return filepath.ToSlash(rel)
}

// inDiff reports whether a hit is inside the changed lines. In full mode every
// hit qualifies. Diff mode is a filter on gating only — the database still
// learns about findings outside the diff.
func inDiff(h finding.Hit, opts Options) bool {
	if opts.DiffBase == "" {
		return true
	}
	for _, line := range opts.ChangedLines[h.File] {
		if line == h.Line {
			return true
		}
	}
	return false
}

// detectorOf recovers the detector name from a namespaced rule id.
func detectorOf(ruleID string) string {
	for i := 0; i < len(ruleID); i++ {
		if ruleID[i] == '/' {
			return ruleID[:i]
		}
	}
	return ruleID
}

func newRunID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
