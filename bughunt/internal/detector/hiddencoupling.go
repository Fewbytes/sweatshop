package detector

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/gitinfo"
)

// Hidden-coupling ratio (research-docs/llm-code-quality/README.md section 5,
// metric 3): co-change Go file pairs without a static dependency edge, divided
// by all co-change pairs. Definitions:
//   - window: the last couplingWindowCommits commits reachable from HEAD;
//   - commits touching more than couplingMaxFilesPerCommit files are skipped,
//     since mass renames and formatting runs say nothing about coupling;
//   - a pair co-changes when both files appear in at least
//     couplingMinCochanges counted commits;
//   - a static edge exists when the files share a directory (one Go package)
//     or either file imports the other's package.
//
// Only files that still exist in the working tree are considered.
const (
	couplingWindowCommits     = 500
	couplingMaxFilesPerCommit = 30
	couplingMinCochanges      = 5
)

// NewHiddenCoupling reports each co-change pair that has no static edge. One
// hit per pair keeps the count directly comparable before and after a change.
// root is the scanned directory; the detector is unavailable when it is not in
// a git repository, since bughunt supports running outside one.
func NewHiddenCoupling(run Runner, root string) Detector {
	return &hiddenCoupling{run: run, root: root}
}

type hiddenCoupling struct {
	run  Runner
	root string
}

func (*hiddenCoupling) Name() string { return "hiddencoupling" }

func (h *hiddenCoupling) Available(context.Context) bool {
	if !lookPath("git") {
		return false
	}
	_, err := gitinfo.RepoRoot(h.root)
	return err == nil
}

type filePair struct{ A, B string } // A < B

func (h *hiddenCoupling) Run(ctx context.Context, dir string, _ []string) ([]finding.Hit, error) {
	tree, err := loadGoTree(dir)
	if err != nil {
		return nil, err
	}
	stdout, stderr, code, _, err := h.run.Run(ctx, "git",
		[]string{"log", "-n", fmt.Sprint(couplingWindowCommits), "--no-renames", "--name-only", "--pretty=format:@@commit"}, dir)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("git log exited %d: %s", code, strings.TrimSpace(stderr))
	}

	cochanges := countCochanges(parseCommitFiles(stdout), tree)
	byPath := map[string]goSource{}
	for _, f := range tree.Files {
		byPath[f.Path] = f
	}

	var pairs []filePair
	for p, n := range cochanges {
		if n >= couplingMinCochanges {
			pairs = append(pairs, p)
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].A != pairs[j].A {
			return pairs[i].A < pairs[j].A
		}
		return pairs[i].B < pairs[j].B
	})

	var hidden []filePair
	for _, p := range pairs {
		if !hasStaticEdge(byPath[p.A], byPath[p.B], tree) {
			hidden = append(hidden, p)
		}
	}
	var hits []finding.Hit
	for _, p := range hidden {
		hits = append(hits, finding.Hit{
			RuleID: "hiddencoupling/cochange",
			File:   p.A,
			Line:   1,
			Message: fmt.Sprintf("%s changed together with %s in %d of the last %d commits with no static dependency (%d of %d co-change pairs hidden)",
				p.A, p.B, cochanges[p], couplingWindowCommits, len(hidden), len(pairs)),
			Severity:  finding.SeverityLow,
			MatchText: p.A + " <-> " + p.B,
		})
	}
	return hits, nil
}

// parseCommitFiles splits `git log --name-only --pretty=format:@@commit`
// output into the file list of each commit.
func parseCommitFiles(out string) [][]string {
	var commits [][]string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "@@commit":
			commits = append(commits, nil)
		case line != "" && len(commits) > 0:
			commits[len(commits)-1] = append(commits[len(commits)-1], line)
		}
	}
	return commits
}

// countCochanges counts, per pair of Go files present in tree, the counted
// commits that touched both.
func countCochanges(commits [][]string, tree goTree) map[filePair]int {
	present := map[string]bool{}
	for _, f := range tree.Files {
		present[f.Path] = true
	}
	counts := map[filePair]int{}
	for _, files := range commits {
		if len(files) > couplingMaxFilesPerCommit {
			continue
		}
		var kept []string
		for _, f := range files {
			if present[f] {
				kept = append(kept, f)
			}
		}
		sort.Strings(kept)
		for i := range kept {
			for j := i + 1; j < len(kept); j++ {
				counts[filePair{kept[i], kept[j]}]++
			}
		}
	}
	return counts
}

func hasStaticEdge(a, b goSource, tree goTree) bool {
	if a.Dir == b.Dir {
		return true
	}
	return importsDir(a, b.Dir, tree) || importsDir(b, a.Dir, tree)
}

func importsDir(from goSource, dir string, tree goTree) bool {
	for _, imp := range importedPaths(from.File) {
		if tree.DirByImportPath[imp] == dir {
			return true
		}
	}
	return false
}
