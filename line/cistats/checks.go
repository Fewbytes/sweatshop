package cistats

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/line/report"
)

// CheckFilter narrows ComputeChecks; a zero field matches everything.
type CheckFilter struct {
	Since  time.Time
	Check  string
	Commit string
}

// CheckSummary is one required check's record over the filtered runs.
type CheckSummary struct {
	Check         string   `json:"check"`
	Runs          int      `json:"runs"`
	Passed        int      `json:"passed"`
	Failed        int      `json:"failed"`
	FailureRate   float64  `json:"failure_rate"` // failed / runs
	DurationP50MS int64    `json:"duration_p50_ms"`
	DurationP95MS int64    `json:"duration_p95_ms"`
	DurationMaxMS int64    `json:"duration_max_ms"`
	FlakyCommits  []string `json:"flaky_commits"` // commits where the check both passed and failed
}

// ComputeChecks summarises the check results matching f, one entry per check
// name, sorted by name.
func ComputeChecks(rows []report.CheckRow, f CheckFilter) []CheckSummary {
	type agg struct {
		durations      []int64
		passed, failed int
		outcomes       map[string][2]bool // commit -> seen [pass, fail]
	}
	byCheck := map[string]*agg{}
	for _, r := range rows {
		if r.StartedAt.Before(f.Since) || f.Check != "" && r.Check != f.Check || f.Commit != "" && r.Commit != f.Commit {
			continue
		}
		a := byCheck[r.Check]
		if a == nil {
			a = &agg{outcomes: map[string][2]bool{}}
			byCheck[r.Check] = a
		}
		a.durations = append(a.durations, r.DurationMS)
		seen := a.outcomes[r.Commit]
		if r.ExitCode == 0 {
			a.passed++
			seen[0] = true
		} else {
			a.failed++
			seen[1] = true
		}
		a.outcomes[r.Commit] = seen
	}
	out := []CheckSummary{}
	for name, a := range byCheck {
		runs := a.passed + a.failed
		s := CheckSummary{Check: name, Runs: runs, Passed: a.passed, Failed: a.failed,
			FailureRate:   float64(a.failed) / float64(runs),
			DurationP50MS: percentile(a.durations, 0.5), DurationP95MS: percentile(a.durations, 0.95),
			FlakyCommits: []string{}}
		for _, d := range a.durations {
			s.DurationMaxMS = max(s.DurationMaxMS, d)
		}
		for commit, seen := range a.outcomes {
			if seen[0] && seen[1] {
				s.FlakyCommits = append(s.FlakyCommits, commit)
			}
		}
		sort.Strings(s.FlakyCommits)
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Check < out[j].Check })
	return out
}

// CheckText renders check summaries for `sweatshop line ci stats`.
func CheckText(sums []CheckSummary) string {
	if len(sums) == 0 {
		return "no check runs\n"
	}
	var b strings.Builder
	for _, s := range sums {
		fmt.Fprintf(&b, "%s: runs %d passed %d failed %d (%.1f%% failing) duration p50 %s p95 %s max %s\n",
			s.Check, s.Runs, s.Passed, s.Failed, s.FailureRate*100, ms(s.DurationP50MS), ms(s.DurationP95MS), ms(s.DurationMaxMS))
		if len(s.FlakyCommits) > 0 {
			fmt.Fprintf(&b, "  flaky on commits: %s\n", strings.Join(s.FlakyCommits, " "))
		}
	}
	return b.String()
}
