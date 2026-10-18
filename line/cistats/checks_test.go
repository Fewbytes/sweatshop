package cistats

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/report"
)

func checkRow(commit, check string, at time.Duration, exit int, ms int64) report.CheckRow {
	return report.CheckRow{RunID: commit, Commit: commit, StartedAt: t0.Add(at), Check: check, ExitCode: exit, DurationMS: ms}
}

func TestComputeChecksReportsDurationFailureRateAndFlakyCommits(t *testing.T) {
	rows := []report.CheckRow{
		checkRow("c1", "test", 0, 1, 100_000),
		checkRow("c1", "test", time.Hour, 0, 300_000), // same commit passed after failing: flake
		checkRow("c2", "test", 2*time.Hour, 0, 200_000),
		checkRow("c2", "lint", 2*time.Hour, 0, 5_000),
		checkRow("c3", "test", 3*time.Hour, 1, 400_000), // fails on its only run: not a flake
	}
	got := ComputeChecks(rows, CheckFilter{})
	want := []CheckSummary{
		{Check: "lint", Runs: 1, Passed: 1, DurationP50MS: 5_000, DurationP95MS: 5_000, DurationMaxMS: 5_000, FlakyCommits: []string{}},
		{Check: "test", Runs: 4, Passed: 2, Failed: 2, FailureRate: 0.5,
			DurationP50MS: 200_000, DurationP95MS: 400_000, DurationMaxMS: 400_000, FlakyCommits: []string{"c1"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ComputeChecks = %+v, want %+v", got, want)
	}
}

func TestComputeChecksFiltersBySinceCheckAndCommit(t *testing.T) {
	rows := []report.CheckRow{
		checkRow("c1", "test", 0, 1, 1_000),
		checkRow("c2", "test", 2*time.Hour, 0, 2_000),
		checkRow("c2", "lint", 2*time.Hour, 0, 3_000),
	}
	got := ComputeChecks(rows, CheckFilter{Since: t0.Add(time.Hour), Check: "test", Commit: "c2"})
	if len(got) != 1 || got[0].Check != "test" || got[0].Runs != 1 || got[0].DurationMaxMS != 2_000 {
		t.Fatalf("filtered = %+v, want only c2's test run", got)
	}
	if got := ComputeChecks(rows, CheckFilter{Commit: "nope"}); len(got) != 0 {
		t.Fatalf("unknown commit = %+v, want none", got)
	}
}

func TestCheckTextNamesFlakyCommits(t *testing.T) {
	text := CheckText([]CheckSummary{{Check: "test", Runs: 2, Passed: 1, Failed: 1, FailureRate: 0.5, FlakyCommits: []string{"c1"}}})
	if !strings.Contains(text, "test: runs 2 passed 1 failed 1 (50.0% failing)") || !strings.Contains(text, "flaky on commits: c1") {
		t.Fatalf("text = %q", text)
	}
}
