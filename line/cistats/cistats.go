// Package cistats answers questions about CI health from the line's stored
// runs and per-test results (report.JSONLRunStore, report.JSONLTestStore):
// pass rate, duration percentiles, slow and failing tests, flakes and
// time-to-green. Infra runs (container, timeout, cord) are counted apart and
// never count against the pass rate or any test.
package cistats

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/line/report"
)

// listLimit bounds every ranked list in a Summary, and with it the number of
// per-test Prometheus series.
const listLimit = 10

// MetricsWindow is the span the Prometheus gauges cover.
const MetricsWindow = 7 * 24 * time.Hour

// Summary is the statistics of the runs and tests since a point in time.
type Summary struct {
	Since            time.Time         `json:"since"` // zero: all stored history
	Runs             int               `json:"runs"`
	Passed           int               `json:"passed"`
	Failed           int               `json:"failed"`
	Infra            int               `json:"infra"`
	PassRate         float64           `json:"pass_rate"` // passed / (passed + failed); NaN-free: 0 when no run judged a commit
	DurationP50MS    int64             `json:"duration_p50_ms"`
	DurationP95MS    int64             `json:"duration_p95_ms"`
	DailyDurations   []DayDuration     `json:"daily_durations"`
	FailedNoTest     int               `json:"failed_without_failing_test"` // failed runs with no failing test: lint, build, or runs saved before per-test results
	SlowestTests     []TestDuration    `json:"slowest_tests"`
	TestFailureRates []TestFailure     `json:"test_failure_rates"`
	FlakyTests       int               `json:"flaky_tests"` // all of them; Flakes lists the top few
	Flakes           []Flake           `json:"flakes"`
	TimeToGreen      []RoleTimeToGreen `json:"time_to_green"`
}

// DayDuration is the duration trend point of one UTC day.
type DayDuration struct {
	Day   string `json:"day"` // 2006-01-02
	Runs  int    `json:"runs"`
	P50MS int64  `json:"p50_ms"`
	P95MS int64  `json:"p95_ms"`
}

// TestDuration is a test's mean and worst duration over its runs.
type TestDuration struct {
	Test   string `json:"test"` // package.Name
	Runs   int    `json:"runs"`
	MeanMS int64  `json:"mean_ms"`
	MaxMS  int64  `json:"max_ms"`
}

// TestFailure is how often a test failed among the runs that executed it.
type TestFailure struct {
	Test     string  `json:"test"`
	Runs     int     `json:"runs"`
	Failures int     `json:"failures"`
	Rate     float64 `json:"rate"`
}

// Flake is a test that both passed and failed on the same commit or tree.
type Flake struct {
	Test  string `json:"test"`
	Trees int    `json:"trees"` // distinct commits-or-trees where it did both
}

// RoleTimeToGreen measures, per role, the time from a failed run to the next
// passing run by that role. Open episodes (still red) are counted but have no
// duration yet.
type RoleTimeToGreen struct {
	Role     string `json:"role"`
	Episodes int    `json:"episodes"` // closed: a failure followed by a pass
	Open     int    `json:"open"`
	P50MS    int64  `json:"p50_ms"`
	MaxMS    int64  `json:"max_ms"`
}

// Compute summarises runs and tests that started at or after since (zero: all).
func Compute(runs []report.Stats, tests []report.TestRow, since time.Time) Summary {
	sum := Summary{Since: since, DailyDurations: []DayDuration{}, SlowestTests: []TestDuration{},
		TestFailureRates: []TestFailure{}, Flakes: []Flake{}, TimeToGreen: []RoleTimeToGreen{}}
	var kept []report.Stats
	for _, r := range runs {
		if !r.StartedAt.Before(since) {
			kept = append(kept, r)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].StartedAt.Before(kept[j].StartedAt) })

	var durations []int64
	byDay := map[string][]int64{}
	failedRuns := map[string]bool{} // RunID of failed runs
	for _, r := range kept {
		sum.Runs++
		switch r.Result() {
		case report.RunPassed:
			sum.Passed++
		case report.RunFailed:
			sum.Failed++
			failedRuns[r.RunID] = true
		case report.RunInfra:
			sum.Infra++
			continue // an infra run's duration is how long it took to break
		}
		durations = append(durations, r.DurationMS)
		day := r.StartedAt.UTC().Format("2006-01-02")
		byDay[day] = append(byDay[day], r.DurationMS)
	}
	if judged := sum.Passed + sum.Failed; judged > 0 {
		sum.PassRate = float64(sum.Passed) / float64(judged)
	}
	sum.DurationP50MS, sum.DurationP95MS = percentile(durations, 0.5), percentile(durations, 0.95)
	days := make([]string, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Strings(days)
	for _, d := range days {
		sum.DailyDurations = append(sum.DailyDurations, DayDuration{Day: d, Runs: len(byDay[d]),
			P50MS: percentile(byDay[d], 0.5), P95MS: percentile(byDay[d], 0.95)})
	}

	type testAgg struct {
		runs, failures int
		totalMS, maxMS int64
	}
	aggs := map[string]*testAgg{}
	outcomesByGroup := map[string]map[string][2]bool{} // test -> commit/tree -> seen [pass, fail]
	failedWithTest := map[string]bool{}
	for _, t := range tests {
		if t.StartedAt.Before(since) || t.Outcome == report.TestSkipped {
			continue
		}
		name := t.Package + "." + t.Test
		a := aggs[name]
		if a == nil {
			a = &testAgg{}
			aggs[name] = a
		}
		a.runs++
		a.totalMS += t.DurationMS
		a.maxMS = max(a.maxMS, t.DurationMS)
		group := t.Tree
		if group == "" {
			group = t.Commit
		}
		if outcomesByGroup[name] == nil {
			outcomesByGroup[name] = map[string][2]bool{}
		}
		seen := outcomesByGroup[name][group]
		if t.Outcome == report.TestFailed {
			a.failures++
			seen[1] = true
			failedWithTest[t.RunID] = true
		} else {
			seen[0] = true
		}
		outcomesByGroup[name][group] = seen
	}
	for id := range failedRuns {
		if !failedWithTest[id] {
			sum.FailedNoTest++
		}
	}
	for name, a := range aggs {
		sum.SlowestTests = append(sum.SlowestTests, TestDuration{Test: name, Runs: a.runs, MeanMS: a.totalMS / int64(a.runs), MaxMS: a.maxMS})
		if a.failures > 0 {
			sum.TestFailureRates = append(sum.TestFailureRates, TestFailure{Test: name, Runs: a.runs, Failures: a.failures, Rate: float64(a.failures) / float64(a.runs)})
		}
		flakyGroups := 0
		for _, seen := range outcomesByGroup[name] {
			if seen[0] && seen[1] {
				flakyGroups++
			}
		}
		if flakyGroups > 0 {
			sum.Flakes = append(sum.Flakes, Flake{Test: name, Trees: flakyGroups})
		}
	}
	sort.Slice(sum.SlowestTests, func(i, j int) bool {
		a, b := sum.SlowestTests[i], sum.SlowestTests[j]
		return a.MeanMS > b.MeanMS || a.MeanMS == b.MeanMS && a.Test < b.Test
	})
	sort.Slice(sum.TestFailureRates, func(i, j int) bool {
		a, b := sum.TestFailureRates[i], sum.TestFailureRates[j]
		return a.Rate > b.Rate || a.Rate == b.Rate && a.Test < b.Test
	})
	sort.Slice(sum.Flakes, func(i, j int) bool {
		a, b := sum.Flakes[i], sum.Flakes[j]
		return a.Trees > b.Trees || a.Trees == b.Trees && a.Test < b.Test
	})
	sum.FlakyTests = len(sum.Flakes)
	sum.SlowestTests = sum.SlowestTests[:min(listLimit, len(sum.SlowestTests))]
	sum.TestFailureRates = sum.TestFailureRates[:min(listLimit, len(sum.TestFailureRates))]
	sum.Flakes = sum.Flakes[:min(listLimit, len(sum.Flakes))]
	sum.TimeToGreen = timeToGreen(kept)
	return sum
}

// timeToGreen walks each role's runs in time order. Infra runs neither open
// nor close an episode.
func timeToGreen(runs []report.Stats) []RoleTimeToGreen {
	type episode struct {
		redSince time.Time
		red      bool
		closedMS []int64
	}
	byRole := map[string]*episode{}
	for _, r := range runs {
		e := byRole[r.Role]
		if e == nil {
			e = &episode{}
			byRole[r.Role] = e
		}
		switch r.Result() {
		case report.RunFailed:
			if !e.red {
				e.red, e.redSince = true, r.StartedAt
			}
		case report.RunPassed:
			if e.red {
				e.closedMS = append(e.closedMS, r.StartedAt.Sub(e.redSince).Milliseconds())
				e.red = false
			}
		}
	}
	out := []RoleTimeToGreen{}
	for role, e := range byRole {
		if len(e.closedMS) == 0 && !e.red {
			continue
		}
		g := RoleTimeToGreen{Role: role, Episodes: len(e.closedMS), P50MS: percentile(e.closedMS, 0.5)}
		if e.red {
			g.Open = 1
		}
		for _, ms := range e.closedMS {
			g.MaxMS = max(g.MaxMS, ms)
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Role < out[j].Role })
	return out
}

// percentile is the nearest-rank percentile of values; 0 for none.
func percentile(values []int64, p float64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[max(rank, 0)]
}

// ParseSince turns a --since value into a start time: a Go duration ("36h")
// or whole days ("7d"), counted back from now. "" means all history.
func ParseSince(value string, now time.Time) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if days, ok := strings.CutSuffix(value, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return time.Time{}, fmt.Errorf("since %q: want a duration like 36h or 7d", value)
		}
		return now.AddDate(0, 0, -n), nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d < 0 {
		return time.Time{}, fmt.Errorf("since %q: want a duration like 36h or 7d", value)
	}
	return now.Add(-d), nil
}

func ms(v int64) string {
	return (time.Duration(v) * time.Millisecond).Round(100 * time.Millisecond).String()
}

// Text renders the summary for `sweatshop line stats`.
func (s Summary) Text() string {
	var b strings.Builder
	window := "all history"
	if !s.Since.IsZero() {
		window = "since " + s.Since.UTC().Format(time.RFC3339)
	}
	fmt.Fprintf(&b, "runs %d (%s)\n", s.Runs, window)
	fmt.Fprintf(&b, "passed %d failed %d infra %d (infra runs are not test failures)\n", s.Passed, s.Failed, s.Infra)
	fmt.Fprintf(&b, "pass rate %.1f%%\n", s.PassRate*100)
	fmt.Fprintf(&b, "duration p50 %s p95 %s\n", ms(s.DurationP50MS), ms(s.DurationP95MS))
	fmt.Fprintf(&b, "failed runs with no failing test %d\n", s.FailedNoTest)
	section := func(title string, n int, line func(i int) string) {
		fmt.Fprintf(&b, "\n%s\n", title)
		if n == 0 {
			b.WriteString("  none\n")
		}
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "  %s\n", line(i))
		}
	}
	section("duration trend (per UTC day)", len(s.DailyDurations), func(i int) string {
		d := s.DailyDurations[i]
		return fmt.Sprintf("%s runs %d p50 %s p95 %s", d.Day, d.Runs, ms(d.P50MS), ms(d.P95MS))
	})
	section("slowest tests", len(s.SlowestTests), func(i int) string {
		t := s.SlowestTests[i]
		return fmt.Sprintf("%s mean %s max %s (%d runs)", t.Test, ms(t.MeanMS), ms(t.MaxMS), t.Runs)
	})
	section("test failure rate", len(s.TestFailureRates), func(i int) string {
		t := s.TestFailureRates[i]
		return fmt.Sprintf("%s %.1f%% (%d of %d runs)", t.Test, t.Rate*100, t.Failures, t.Runs)
	})
	section("flaky tests (passed and failed on the same tree)", len(s.Flakes), func(i int) string {
		f := s.Flakes[i]
		return fmt.Sprintf("%s on %d trees", f.Test, f.Trees)
	})
	section("time to green after a failure", len(s.TimeToGreen), func(i int) string {
		g := s.TimeToGreen[i]
		return fmt.Sprintf("%s episodes %d p50 %s max %s still red %d", g.Role, g.Episodes, ms(g.P50MS), ms(g.MaxMS), g.Open)
	})
	return b.String()
}

// Prometheus renders the summary in the text exposition format. allTime
// supplies the counters, which must not shrink as the window moves.
func (s Summary) Prometheus(allTime Summary) string {
	var b strings.Builder
	metric := func(name, kind, help string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
	}
	metric("sweatshop_ci_runs_total", "counter", "Witness runs by outcome; infra runs are not test failures.")
	fmt.Fprintf(&b, "sweatshop_ci_runs_total{outcome=\"passed\"} %d\n", allTime.Passed)
	fmt.Fprintf(&b, "sweatshop_ci_runs_total{outcome=\"failed\"} %d\n", allTime.Failed)
	fmt.Fprintf(&b, "sweatshop_ci_runs_total{outcome=\"infra\"} %d\n", allTime.Infra)
	metric("sweatshop_ci_pass_ratio", "gauge", "Passed / (passed + failed) runs over the last 7 days.")
	fmt.Fprintf(&b, "sweatshop_ci_pass_ratio %g\n", s.PassRate)
	metric("sweatshop_ci_run_duration_seconds", "gauge", "Run duration quantiles over the last 7 days.")
	fmt.Fprintf(&b, "sweatshop_ci_run_duration_seconds{quantile=\"0.5\"} %g\n", float64(s.DurationP50MS)/1000)
	fmt.Fprintf(&b, "sweatshop_ci_run_duration_seconds{quantile=\"0.95\"} %g\n", float64(s.DurationP95MS)/1000)
	metric("sweatshop_ci_flaky_tests", "gauge", "Tests that passed and failed on the same tree in the last 7 days ")
	fmt.Fprintf(&b, "sweatshop_ci_flaky_tests %d\n", s.FlakyTests)
	metric("sweatshop_ci_test_failure_ratio", "gauge", "Failure ratio of the 10 most failing tests over the last 7 days.")
	for _, t := range s.TestFailureRates {
		fmt.Fprintf(&b, "sweatshop_ci_test_failure_ratio{test=%q} %g\n", t.Test, t.Rate)
	}
	metric("sweatshop_ci_time_to_green_seconds", "gauge", "Median time from a failed run to the next pass, per role, over the last 7 days.")
	for _, g := range s.TimeToGreen {
		fmt.Fprintf(&b, "sweatshop_ci_time_to_green_seconds{role=%q} %g\n", g.Role, float64(g.P50MS)/1000)
	}
	return b.String()
}
