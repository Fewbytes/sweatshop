package cistats

import (
	"strings"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/report"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func run(id, tree, role string, at time.Duration, ms int64, outcome report.RunOutcome) report.Stats {
	return report.Stats{RunID: id, Commit: id, Tree: tree, Role: role, StartedAt: t0.Add(at), DurationMS: ms,
		Outcome: outcome, Passed: outcome == report.RunPassed}
}

func row(runID, tree, test string, outcome report.TestOutcome, ms int64) report.TestRow {
	return report.TestRow{RunID: runID, Commit: runID, Tree: tree, StartedAt: t0, Package: "p", Test: test, Outcome: outcome, DurationMS: ms}
}

func TestComputeSeparatesInfraFromFailuresAndRatesPasses(t *testing.T) {
	runs := []report.Stats{
		run("r1", "t1", "maker", 0, 100_000, report.RunFailed),
		run("r2", "t1", "maker", time.Hour, 300_000, report.RunPassed),
		run("r3", "t2", "maker", 2*time.Hour, 900_000, report.RunInfra),
		{RunID: "", Commit: "legacy", Tree: "t3", Role: "maker", StartedAt: t0.Add(3 * time.Hour), DurationMS: 200_000, Passed: true},
	}
	sum := Compute(runs, nil, time.Time{})
	if sum.Runs != 4 || sum.Passed != 2 || sum.Failed != 1 || sum.Infra != 1 {
		t.Fatalf("counts = %+v", sum)
	}
	if got := sum.PassRate; got < 0.666 || got > 0.667 {
		t.Errorf("pass rate = %v, want 2/3 (infra excluded)", got)
	}
	if sum.DurationP50MS != 200_000 || sum.DurationP95MS != 300_000 {
		t.Errorf("p50/p95 = %d/%d, want 200000/300000 (infra excluded)", sum.DurationP50MS, sum.DurationP95MS)
	}
	if sum.FailedNoTest != 1 {
		t.Errorf("failed without failing test = %d, want 1", sum.FailedNoTest)
	}
}

func TestComputeFindsFlakesSlowTestsAndFailureRates(t *testing.T) {
	runs := []report.Stats{
		run("r1", "t1", "maker", 0, 1, report.RunFailed),
		run("r2", "t1", "maker", time.Minute, 1, report.RunPassed),
		run("r3", "t2", "maker", 2*time.Minute, 1, report.RunFailed),
	}
	tests := []report.TestRow{
		row("r1", "t1", "TestFlaky", report.TestFailed, 10),
		row("r2", "t1", "TestFlaky", report.TestPassed, 30),
		row("r3", "t2", "TestBroken", report.TestFailed, 5),
		row("r1", "t1", "TestSlow", report.TestPassed, 5000),
		row("r1", "t1", "TestSkip", report.TestSkipped, 0),
	}
	sum := Compute(runs, tests, time.Time{})
	if len(sum.Flakes) != 1 || sum.Flakes[0].Test != "p.TestFlaky" || sum.FlakyTests != 1 {
		t.Errorf("flakes = %+v (TestBroken failed on one tree only)", sum.Flakes)
	}
	if sum.SlowestTests[0].Test != "p.TestSlow" || sum.SlowestTests[0].MeanMS != 5000 {
		t.Errorf("slowest = %+v", sum.SlowestTests)
	}
	if sum.TestFailureRates[0].Test != "p.TestBroken" || sum.TestFailureRates[0].Rate != 1 ||
		sum.TestFailureRates[1].Test != "p.TestFlaky" || sum.TestFailureRates[1].Rate != 0.5 || len(sum.TestFailureRates) != 2 {
		t.Errorf("failure rates = %+v", sum.TestFailureRates)
	}
	if sum.FailedNoTest != 0 {
		t.Errorf("every failed run has a failing test, got %d without", sum.FailedNoTest)
	}
}

func TestTimeToGreenRunsFromFirstFailureToNextPassPerRole(t *testing.T) {
	runs := []report.Stats{
		run("a1", "t1", "andon", 0, 1, report.RunFailed),
		run("m1", "t9", "maker", 10*time.Minute, 1, report.RunPassed), // another role's pass does not close it
		run("a2", "t2", "andon", 20*time.Minute, 1, report.RunFailed),
		run("a3", "t3", "andon", 30*time.Minute, 1, report.RunInfra),
		run("a4", "t4", "andon", 40*time.Minute, 1, report.RunPassed),
		run("a5", "t5", "andon", time.Hour, 1, report.RunFailed),
	}
	got := Compute(runs, nil, time.Time{}).TimeToGreen
	if len(got) != 1 || got[0].Role != "andon" || got[0].Episodes != 1 || got[0].P50MS != (40*time.Minute).Milliseconds() || got[0].Open != 1 {
		t.Fatalf("time to green = %+v", got)
	}
}

func TestComputeSinceDropsOlderRunsAndTests(t *testing.T) {
	runs := []report.Stats{run("old", "t1", "maker", 0, 1, report.RunFailed), run("new", "t2", "maker", 48*time.Hour, 1, report.RunPassed)}
	tests := []report.TestRow{row("old", "t1", "TestX", report.TestFailed, 1)}
	sum := Compute(runs, tests, t0.Add(time.Hour))
	if sum.Runs != 1 || len(sum.TestFailureRates) != 0 {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestParseSince(t *testing.T) {
	now := t0
	for value, want := range map[string]time.Time{"": {}, "36h": now.Add(-36 * time.Hour), "7d": now.AddDate(0, 0, -7)} {
		if got, err := ParseSince(value, now); err != nil || !got.Equal(want) {
			t.Errorf("ParseSince(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	for _, bad := range []string{"soon", "-1h", "xd"} {
		if _, err := ParseSince(bad, now); err == nil {
			t.Errorf("ParseSince(%q) accepted", bad)
		}
	}
}

func TestPrometheusExposesRunCountersAndWindowGauges(t *testing.T) {
	runs := []report.Stats{run("r1", "t1", "maker", 0, 2000, report.RunPassed), run("r2", "t2", "maker", time.Minute, 4000, report.RunFailed)}
	tests := []report.TestRow{row("r2", "t2", "TestBroken", report.TestFailed, 1)}
	out := Compute(runs, tests, time.Time{}).Prometheus(Compute(runs, nil, time.Time{}))
	for _, want := range []string{
		`sweatshop_ci_runs_total{outcome="passed"} 1`,
		`sweatshop_ci_runs_total{outcome="infra"} 0`,
		"sweatshop_ci_pass_ratio 0.5",
		`sweatshop_ci_run_duration_seconds{quantile="0.95"} 4`,
		`sweatshop_ci_test_failure_ratio{test="p.TestBroken"} 1`,
		`sweatshop_ci_time_to_green_seconds{role="maker"}`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics lack %q:\n%s", want, out)
		}
	}
}

func TestTextNamesEachSection(t *testing.T) {
	text := Compute([]report.Stats{run("r1", "t1", "maker", 0, 2000, report.RunPassed)}, nil, time.Time{}).Text()
	for _, want := range []string{"pass rate 100.0%", "duration trend", "slowest tests", "test failure rate", "flaky tests", "time to green"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
}
