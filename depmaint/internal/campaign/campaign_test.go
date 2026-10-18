package campaign

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
	"github.com/Fewbytes/sweatshop/depmaint/internal/planner"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/*.golden from current output")

// fakeRegistry is hand-written published truth: package -> version -> requirements.
type fakeRegistry map[string]map[string][]planner.Requirement

func (f fakeRegistry) Versions(pkg string) ([]string, error) {
	var out []string
	for v := range f[pkg] {
		out = append(out, v)
	}
	return out, nil
}

func (f fakeRegistry) Requirements(pkg, version string) ([]planner.Requirement, error) {
	return f[pkg][version], nil
}

// fakeWorld is a graph the test lands upgrades into; every change gets a
// new snapshot id, as a real re-expansion would.
type fakeWorld struct {
	graph    planner.Graph
	snapshot int
}

func (w *fakeWorld) Graph(_ context.Context, _ []string, _ bool) (planner.Graph, string, error) {
	return w.graph, "snap-" + string(rune('0'+w.snapshot)), nil
}

// land rewrites every node and edge key of pkg to its new version, as an
// executor's successful upgrade followed by re-expansion would.
func (w *fakeWorld) land(pkg, from, to string, requirements map[string]string) {
	rekey := func(k string) string {
		if k == pkg+"@"+from {
			return pkg + "@" + to
		}
		return k
	}
	var g planner.Graph
	for _, n := range w.graph.Nodes {
		n.Key = rekey(n.Key)
		g.Nodes = append(g.Nodes, n)
	}
	for _, e := range w.graph.Edges {
		if e.From == pkg+"@"+from {
			continue // the old version's own requirements go away
		}
		e.To = rekey(e.To)
		g.Edges = append(g.Edges, e)
	}
	for dep, req := range requirements {
		g.Edges = append(g.Edges, expander.Edge{From: pkg + "@" + to, To: dep, Requirement: req})
	}
	w.graph = g
	w.snapshot++
}

// fakeClock only moves when the test advances it.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

type line struct {
	t      *testing.T
	engine Engine
	world  *fakeWorld
	clock  *fakeClock
}

// newLine builds an app whose lib upgrade is blocked by plug: the plan is
// plug 1.0.0->1.1.0 then lib 1.0.0->1.2.0.
func newLine(t *testing.T) *line {
	t.Helper()
	pkg := func(key string, direct bool) expander.Node {
		return expander.Node{Key: key, Kind: "package", Direct: direct}
	}
	world := &fakeWorld{graph: planner.Graph{
		Nodes: []expander.Node{pkg("node:app@1.0.0", false), pkg("node:lib@1.0.0", true), pkg("node:plug@1.0.0", true)},
		Edges: []expander.Edge{
			{From: "node:app@1.0.0", To: "node:lib@1.0.0", Requirement: "^1.0.0"},
			{From: "node:app@1.0.0", To: "node:plug@1.0.0", Requirement: ">=1.0.0"},
			{From: "node:plug@1.0.0", To: "node:lib@1.0.0", Requirement: "~1.0.0"},
		},
	}}
	registry := fakeRegistry{
		"node:lib":  {"1.0.0": nil, "1.2.0": nil},
		"node:plug": {"1.0.0": {{Package: "node:lib", Constraint: "~1.0.0"}}, "1.1.0": {{Package: "node:lib", Constraint: "^1.0.0"}}},
	}
	store, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "campaign.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	clock := &fakeClock{now: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	return &line{t: t, world: world, clock: clock,
		engine: Engine{Store: store, World: world, Registry: registry, Accepts: planner.SemverAccepts, Now: clock.Now}}
}

func (l *line) newPlannedCampaign(maxAttempts int) Campaign {
	l.t.Helper()
	c, err := l.engine.New(context.Background(), NewSpec{Type: TypeUpgrade, Package: "node:lib", Target: "1.2.0",
		Roots: []string{"."}, Budget: Budget{MaxAttempts: maxAttempts, MaxDuration: time.Hour}})
	if err != nil {
		l.t.Fatalf("New: %v", err)
	}
	if c, err = l.engine.Plan(context.Background(), c.ID, false); err != nil {
		l.t.Fatalf("Plan: %v", err)
	}
	return c
}

func (l *line) next(id string) Next {
	l.t.Helper()
	n, err := l.engine.Next(context.Background(), id)
	if err != nil {
		l.t.Fatalf("Next: %v", err)
	}
	return n
}

func (l *line) record(id string, step int, verdict Verdict, reason string) Recorded {
	l.t.Helper()
	r, err := l.engine.Record(context.Background(), RecordInput{CampaignID: id, Step: step, Verdict: verdict, Reason: reason, Executor: "agent-a"})
	if err != nil {
		l.t.Fatalf("Record: %v", err)
	}
	return r
}

func requireState(t *testing.T, n Next, want NextState) {
	t.Helper()
	if n.State != want {
		t.Fatalf("next state = %q (%s), want %q", n.State, n.Reason, want)
	}
}

func requireGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if string(want) != got {
		t.Errorf("work order differs from %s\n--- want\n%s--- got\n%s", path, want, got)
	}
}

func TestNextIssuesTheSameGoldenWorkOrderWhileStepIsInFlight(t *testing.T) {
	l := newLine(t)
	c := l.newPlannedCampaign(5)

	first, second := l.next(c.ID), l.next(c.ID)

	requireState(t, first, NextDispatch)
	if first.Step != 1 || first.Steps != 2 {
		t.Errorf("step %d of %d, want 1 of 2", first.Step, first.Steps)
	}
	if first.WorkOrder != second.WorkOrder {
		t.Error("work order changed between two next calls with no verdict in between")
	}
	requireGolden(t, "plug-first", first.WorkOrder)
}

func TestSuccessfulStepsReplanAndFinishTheCampaign(t *testing.T) {
	l := newLine(t)
	c := l.newPlannedCampaign(5)

	l.world.land("node:plug", "1.0.0", "1.1.0", map[string]string{"node:lib": "^1.0.0"})
	r := l.record(c.ID, 1, VerdictSucceeded, "")
	if r.Campaign.Completed != 1 || r.Attempt.SnapshotAfter != "snap-1" {
		t.Fatalf("after step 1: completed=%d snapshot_after=%q", r.Campaign.Completed, r.Attempt.SnapshotAfter)
	}
	n := l.next(c.ID)
	requireState(t, n, NextDispatch)
	if n.Step != 2 || n.Steps != 2 || !strings.Contains(n.WorkOrder, "move node:lib from 1.0.0 to 1.2.0") {
		t.Fatalf("want step 2 of 2 moving lib, got step %d of %d:\n%s", n.Step, n.Steps, n.WorkOrder)
	}
	requireGolden(t, "lib-second", n.WorkOrder)

	l.world.land("node:lib", "1.0.0", "1.2.0", nil)
	l.record(c.ID, 2, VerdictSucceeded, "")
	requireState(t, l.next(c.ID), NextSucceeded)
}

func TestRejectedStepIsNotReissuedUntilTheGraphChanges(t *testing.T) {
	l := newLine(t)
	c := l.newPlannedCampaign(5)

	l.record(c.ID, 1, VerdictRejected, "plug 1.1.0 drops the v1 hooks API")
	blocked := l.next(c.ID)
	requireState(t, blocked, NextBlocked)
	if !strings.Contains(blocked.Reason, "plug 1.1.0 drops the v1 hooks API") {
		t.Errorf("blocked reason should carry the rejection, got %q", blocked.Reason)
	}

	// The graph changes (someone landed a patch bump), so the identical step
	// is a new (step, snapshot, plan) and may be tried again.
	l.world.land("node:plug", "1.0.0", "1.0.0", map[string]string{"node:lib": "~1.0.0"})
	if _, err := l.engine.Plan(context.Background(), c.ID, false); err != nil {
		t.Fatal(err)
	}
	n := l.next(c.ID)
	requireState(t, n, NextDispatch)
	if !strings.Contains(n.WorkOrder, "plug 1.0.0 -> 1.1.0") && !strings.Contains(n.WorkOrder, "from 1.0.0 to 1.1.0") {
		t.Errorf("re-issued order should still be the plug step:\n%s", n.WorkOrder)
	}
}

func TestLateVerdictFromRacingExecutorIsSupersededAndNeverSuppresses(t *testing.T) {
	l := newLine(t)
	c := l.newPlannedCampaign(5)

	l.world.land("node:plug", "1.0.0", "1.1.0", map[string]string{"node:lib": "^1.0.0"})
	l.record(c.ID, 1, VerdictSucceeded, "")
	late := l.record(c.ID, 1, VerdictRejected, "my build broke")

	if late.Attempt.Verdict != VerdictSuperseded {
		t.Fatalf("late verdict = %q, want superseded", late.Attempt.Verdict)
	}
	if late.Campaign.Completed != 1 {
		t.Errorf("a superseded verdict must not re-plan or advance; completed = %d", late.Campaign.Completed)
	}
	requireState(t, l.next(c.ID), NextDispatch)
	if late.Campaign.Spent != 2 {
		t.Errorf("racing is spending budget; spent = %d, want 2", late.Campaign.Spent)
	}
}

func TestBudgetExhaustionStopsDispatchByAttemptsAndByClock(t *testing.T) {
	l := newLine(t)
	byAttempts := l.newPlannedCampaign(1)
	l.record(byAttempts.ID, 1, VerdictFailed, "network down")
	requireState(t, l.next(byAttempts.ID), NextExhausted)

	byClock := l.newPlannedCampaign(9)
	requireState(t, l.next(byClock.ID), NextDispatch)
	l.clock.Advance(time.Hour)
	requireState(t, l.next(byClock.ID), NextExhausted)
}

func TestFailedStepMayBeRedispatched(t *testing.T) {
	l := newLine(t)
	c := l.newPlannedCampaign(5)

	l.record(c.ID, 1, VerdictFailed, "flaky build")

	requireState(t, l.next(c.ID), NextDispatch)
}

func TestInfeasiblePlanBlocksTheCampaign(t *testing.T) {
	l := newLine(t)
	l.engine.Registry = fakeRegistry{
		"node:lib":  {"1.0.0": nil, "1.2.0": nil},
		"node:plug": {"1.0.0": {{Package: "node:lib", Constraint: "~1.0.0"}}},
	}
	c := l.newPlannedCampaign(5)

	requireState(t, l.next(c.ID), NextBlocked)
}

func TestRecordRejectsMalformedReports(t *testing.T) {
	l := newLine(t)
	c := l.newPlannedCampaign(5)
	cases := map[string]RecordInput{
		"rejection without reason": {CampaignID: c.ID, Step: 1, Verdict: VerdictRejected},
		"findings verdict":         {CampaignID: c.ID, Step: 1, Verdict: "findings"},
		"step beyond the live one": {CampaignID: c.ID, Step: 2, Verdict: VerdictFailed},
		"unknown campaign":         {CampaignID: "dep_c99", Step: 1, Verdict: VerdictFailed},
	}
	for name, in := range cases {
		if _, err := l.engine.Record(context.Background(), in); err == nil {
			t.Errorf("%s: want an error, got none", name)
		}
	}
}

func TestNextRefusesWhenTheGraphMovedSincePlanning(t *testing.T) {
	l := newLine(t)
	c := l.newPlannedCampaign(5)

	l.world.snapshot++

	if _, err := l.engine.Next(context.Background(), c.ID); err == nil || !strings.Contains(err.Error(), "campaign plan") {
		t.Fatalf("want an error naming `campaign plan`, got %v", err)
	}
}
