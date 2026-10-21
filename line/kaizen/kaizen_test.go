package kaizen

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/gitx/gittest"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/guidance"
	"github.com/Fewbytes/sweatshop/line/linedir"
	"github.com/Fewbytes/sweatshop/line/maker"
	"github.com/Fewbytes/sweatshop/line/station"
)

const goodReport = `refactor: unify retry backoff

Target:    pkg/retry
Category:  P2 Duplicated knowledge
Hotspot:   churn=9 complexity=14
Change:    unify retry backoff into retry.Policy
Signals:   clones: 4 → 1
Safety:    go test ./pkg/..., none added
Behavior:  unchanged (tests: 41), public API unchanged
Follow-ups: none

Bead: kaizen-20261013T000000Z
`

func TestPromptEmbedsKaizenGuidanceAndTrailer(t *testing.T) {
	guide, _ := guidance.For("kaizen")
	p := Prompt("kaizen-x")
	for _, want := range []string{guide, "Bead: kaizen-x", "Signals:   <signal>: <before> → <after>"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}

// The prompt repeats the report template so the agent sees it next to the
// protocol; this fails if the guide's template changes and the copy does not.
func TestReportTemplateMatchesGuide(t *testing.T) {
	_, template, found := strings.Cut(guidance.Kaizen, "## 6. Change report template")
	if !found {
		t.Fatal("kaizen guide has no change report template section")
	}
	for _, f := range reportFields {
		if !strings.Contains(template, f+":") {
			t.Errorf("guide template lacks field %q", f)
		}
		if !strings.Contains(Prompt("x"), "   "+f+":") {
			t.Errorf("prompt template lacks field %q", f)
		}
	}
}

func TestCheckReport(t *testing.T) {
	if err := CheckReport(goodReport); err != nil {
		t.Fatalf("good report: %v", err)
	}
	cases := map[string]string{
		"missing field":    strings.Replace(goodReport, "Safety:    go test ./pkg/..., none added\n", "", 1),
		"no before→after":  strings.Replace(goodReport, "clones: 4 → 1", "clones improved", 1),
		"template left in": strings.Replace(goodReport, "clones: 4 → 1", "<signal>: <before> → <after>", 1),
		"signal unchanged": strings.Replace(goodReport, "clones: 4 → 1", "clones: 4 → 4", 1),
		"no report":        "refactor: things\n\nBead: kaizen-x\n",
	}
	for name, msg := range cases {
		if CheckReport(msg) == nil {
			t.Errorf("%s: CheckReport accepted %q", name, msg)
		}
	}
}

func TestCheckReportAcceptsThePackageQualifiedLineTheScriptsPrint(t *testing.T) {
	msg := strings.Replace(goodReport, "clones: 4 → 1", "line/station hotspot: 85573 → 61200", 1)
	if err := CheckReport(msg); err != nil {
		t.Fatal(err)
	}
}

// The prompt tells the agent to run the scripts at signalsScript with the pinned
// signalsRequirements; the Python
// unit tests are part of `just test` through this test.
func TestSignalsScriptExistsAndItsTestsPass(t *testing.T) {
	for _, path := range []string{signalsScript, signalsRequirements} {
		if _, err := os.Stat(filepath.Join("..", "..", path)); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(Prompt("x"), path) {
			t.Errorf("prompt does not name %s", path)
		}
	}
	out, err := exec.Command("uv", "run", "--no-project", "--with-requirements", filepath.Join("..", "..", signalsRequirements),
		"python", "-m", "unittest", "discover", "-s", "signals", "-t", "signals").CombinedOutput()
	if err != nil {
		t.Fatalf("signal script tests failed: %v\n%s", err, out)
	}
}

// docs/modules.json is the committed module map; it must agree with the tree
// on every field that is a function of the files (kaizen.md section 3).
func TestCommittedModuleMapAgreesWithTheTree(t *testing.T) {
	root := filepath.Join("..", "..")
	out, err := exec.Command("uv", "run", "--no-project", "--with-requirements", filepath.Join(root, signalsRequirements),
		filepath.Join(root, signalsScript), "modules-check", "--root", root, "--map", filepath.Join(root, "docs", "modules.json")).CombinedOutput()
	if err != nil {
		t.Fatalf("module map disagrees with the tree: %v\n%s", err, out)
	}
}

func seededRemote(t *testing.T) (bare, work gitx.Repo) {
	t.Helper()
	ctx := context.Background()
	run := func(r gitx.Repo, args ...string) {
		t.Helper()
		if _, err := gittest.Git(ctx, r.Dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	bare = gitx.Repo{Dir: filepath.Join(t.TempDir(), "remote.git")}
	run(gitx.Repo{Dir: t.TempDir()}, "init", "--quiet", "--bare", "--initial-branch=master", bare.Dir)
	work = gitx.Repo{Dir: t.TempDir()}
	run(work, "init", "--quiet", "--initial-branch=master")
	run(work, "config", "user.name", "test")
	run(work, "config", "user.email", "test@example.com")
	run(work, "remote", "add", "origin", bare.Dir)
	run(work, "commit", "--quiet", "--allow-empty", "-m", "seed")
	run(work, "push", "--quiet", "origin", "master")
	return bare, work
}

func TestCheckCommitReportReadsHeadMessage(t *testing.T) {
	_, work := seededRemote(t)
	ctx := context.Background()
	if _, err := gittest.Git(ctx, work.Dir, "commit", "--quiet", "--allow-empty", "-m", "no report"); err != nil {
		t.Fatal(err)
	}
	if CheckCommitReport(ctx, work, "HEAD") == nil {
		t.Fatal("accepted a commit without a report")
	}
	if _, err := gittest.Git(ctx, work.Dir, "commit", "--quiet", "--allow-empty", "-m", goodReport); err != nil {
		t.Fatal(err)
	}
	if err := CheckCommitReport(ctx, work, "HEAD"); err != nil {
		t.Fatal(err)
	}
}

type fakeEngine struct {
	specs []maker.ContainerSpec
	run   func(maker.ContainerSpec)
}

func (f *fakeEngine) Run(_ context.Context, spec maker.ContainerSpec) (int, error) {
	f.specs = append(f.specs, spec)
	if f.run != nil {
		f.run(spec)
	}
	return 0, nil
}

func newRunner(t *testing.T, bare gitx.Repo, e *fakeEngine, now *time.Time) *Runner {
	t.Helper()
	return &Runner{
		Line:    linedir.Line{Remote: bare.Dir, Image: "img", Port: 1},
		Station: linedir.Station{Name: "kaizen-1", Role: "kaizen", AgentID: "kaizen-1"},
		Token:   "tok",
		Engine:  e,
		Now:     func() time.Time { return *now },
	}
}

func moveMaster(t *testing.T, work gitx.Repo, msg string) {
	t.Helper()
	ctx := context.Background()
	if _, err := gittest.Git(ctx, work.Dir, "commit", "--quiet", "--allow-empty", "-m", msg); err != nil {
		t.Fatal(err)
	}
	if _, err := gittest.Git(ctx, work.Dir, "push", "--quiet", "origin", "master"); err != nil {
		t.Fatal(err)
	}
}

func TestOnceRunsOnlyWhenIntervalPassedAndMasterMoved(t *testing.T) {
	ctx := context.Background()
	bare, work := seededRemote(t)
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	e := &fakeEngine{}
	r := newRunner(t, bare, e, &now)
	r.Interval = time.Hour

	steps := []struct {
		name    string
		advance time.Duration
		move    bool
		wantRun bool
	}{
		{"first run starts at once", 0, false, true},
		{"interval passed but master unmoved", 2 * time.Hour, false, false},
		{"master moved but interval not passed", -90 * time.Minute, true, false},
		{"both", 2 * time.Hour, false, true},
	}
	for _, s := range steps {
		now = now.Add(s.advance)
		if s.move {
			moveMaster(t, work, "feature")
		}
		ran, err := r.Once(ctx)
		if err != nil || ran != s.wantRun {
			t.Fatalf("%s: Once = %v, %v; want ran=%v", s.name, ran, err, s.wantRun)
		}
	}
	if len(e.specs) != 2 {
		t.Fatalf("engine ran %d times, want 2", len(e.specs))
	}
}

func TestOnceSleepsWhileAndonIsPulled(t *testing.T) {
	ctx := context.Background()
	bare, _ := seededRemote(t)
	master, err := bare.Ref(ctx, "refs/heads/master")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gittest.Git(ctx, bare.Dir, "update-ref", guard.StopRef, master); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e := &fakeEngine{}
	if ran, err := newRunner(t, bare, e, &now).Once(ctx); err != nil || ran || len(e.specs) != 0 {
		t.Fatalf("Once = %v, %v with %d runs; want no run while the cord is pulled", ran, err, len(e.specs))
	}
}

func TestOnceContainerGetsRunIDPromptAndKaizenMarker(t *testing.T) {
	bare, _ := seededRemote(t)
	now := time.Date(2026, 10, 13, 1, 2, 3, 0, time.UTC)
	e := &fakeEngine{}
	if _, err := newRunner(t, bare, e, &now).Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	env := e.specs[0].Env
	if env["SWEATSHOP_BEAD"] != "kaizen-20261013T010203Z" || env["SWEATSHOP_KAIZEN"] != "1" {
		t.Errorf("env = %v, want run ID as bead and the kaizen marker", env)
	}
	if !strings.Contains(env["SWEATSHOP_PROMPT"], "Bead: kaizen-20261013T010203Z") {
		t.Errorf("prompt does not carry the run's trailer")
	}
	if env["SWEATSHOP_LINE_TOKEN"] != "tok" {
		t.Errorf("token not passed to the container")
	}
}

type fakeHeartbeats struct{ beat []station.Heartbeat }

func (f *fakeHeartbeats) Heartbeat(_ context.Context, hb station.Heartbeat) error {
	hb.Clock = nil
	f.beat = append(f.beat, hb)
	return nil
}

func TestOnceHeartbeatsScheduleIdleThenWorkingTheRun(t *testing.T) {
	bare, _ := seededRemote(t)
	start := time.Date(2026, 10, 13, 1, 2, 3, 0, time.UTC)
	now := start
	hb := &fakeHeartbeats{}
	r := newRunner(t, bare, &fakeEngine{}, &now)
	r.Heartbeats, r.Commit, r.Interval = hb, "abc123", time.Hour

	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = start.Add(time.Minute)
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}

	next := start.Add(time.Hour)
	want := []station.Heartbeat{
		{State: station.StateIdle, Commit: "abc123", NextRun: &start}, // never run: eligible now
		{State: station.StateWorking, Card: "kaizen-20261013T010203Z", Commit: "abc123", NextRun: &start},
		{State: station.StateIdle, Commit: "abc123", LastRun: &start, NextRun: &next},
	}
	if !reflect.DeepEqual(hb.beat, want) {
		t.Fatalf("heartbeats %+v, want %+v", hb.beat, want)
	}
}

type fakeRequests struct {
	status  station.Status
	answers []station.KaizenAnswer
}

func (f *fakeRequests) Status(context.Context) (station.Status, error) { return f.status, nil }
func (f *fakeRequests) AnswerKaizen(_ context.Context, a station.KaizenAnswer) error {
	f.answers = append(f.answers, a)
	return nil
}

func pendingRequest() station.Status {
	return station.Status{KaizenRequest: &station.KaizenRequest{State: station.KaizenRequestPending}}
}

func TestOnceRunsARequestedRunBeforeTheIntervalAndAnswersStarted(t *testing.T) {
	ctx := context.Background()
	bare, work := seededRemote(t)
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	e := &fakeEngine{}
	reqs := &fakeRequests{}
	r := newRunner(t, bare, e, &now)
	r.Requests = reqs
	if ran, err := r.Once(ctx); err != nil || !ran {
		t.Fatalf("first run: %v, %v", ran, err)
	}
	moveMaster(t, work, "feature")
	now = now.Add(time.Minute) // well inside the 24h interval
	if ran, err := r.Once(ctx); err != nil || ran {
		t.Fatalf("unrequested run inside the interval: %v, %v", ran, err)
	}
	reqs.status = pendingRequest()
	if ran, err := r.Once(ctx); err != nil || !ran {
		t.Fatalf("requested run: %v, %v", ran, err)
	}
	if want := []station.KaizenAnswer{{State: station.KaizenRequestStarted}}; !reflect.DeepEqual(reqs.answers, want) {
		t.Fatalf("answers %+v, want %+v", reqs.answers, want)
	}
}

func TestOnceDeclinesARequestWhenMasterHasNotMoved(t *testing.T) {
	ctx := context.Background()
	bare, _ := seededRemote(t)
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	e := &fakeEngine{}
	reqs := &fakeRequests{}
	r := newRunner(t, bare, e, &now)
	r.Requests = reqs
	if _, err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	reqs.status = pendingRequest()
	if ran, err := r.Once(ctx); err != nil || ran || len(e.specs) != 1 {
		t.Fatalf("Once = %v, %v with %d runs; want a decline and no new run", ran, err, len(e.specs))
	}
	want := []station.KaizenAnswer{{State: station.KaizenRequestDeclined, Reason: "master has not moved since the last run"}}
	if !reflect.DeepEqual(reqs.answers, want) {
		t.Fatalf("answers %+v, want %+v", reqs.answers, want)
	}
}

func TestOnceDeclinesARequestWhileAndonIsPulled(t *testing.T) {
	ctx := context.Background()
	bare, _ := seededRemote(t)
	master, err := bare.Ref(ctx, "refs/heads/master")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gittest.Git(ctx, bare.Dir, "update-ref", guard.StopRef, master); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e := &fakeEngine{}
	reqs := &fakeRequests{status: pendingRequest()}
	r := newRunner(t, bare, e, &now)
	r.Requests = reqs
	if ran, err := r.Once(ctx); err != nil || ran || len(e.specs) != 0 {
		t.Fatalf("Once = %v, %v with %d runs", ran, err, len(e.specs))
	}
	want := []station.KaizenAnswer{{State: station.KaizenRequestDeclined, Reason: "the andon cord is pulled"}}
	if !reflect.DeepEqual(reqs.answers, want) {
		t.Fatalf("answers %+v, want %+v", reqs.answers, want)
	}
}

func TestPromptOrdersBaselineStatusPickChangeAndMeasureAgain(t *testing.T) {
	p := Prompt("kaizen-x")
	steps := []string{
		"sweatshop line kaizen context",
		"$S measure --root . --rev $BASE --runs $D/runs.json --out $D/signals.json",
		"sweatshop line kaizen publish --commit $BASE --signals $D/signals.json --report",
		"sweatshop line kaizen report`",
		"--out $D/after.json",
		"$S delta",
		"--after $D/after.json --report",
		"sweatshop line merge",
	}
	last := -1
	for _, step := range steps {
		at := strings.Index(p, step)
		if at <= last {
			t.Fatalf("step %q at %d, not after the previous step at %d", step, at, last)
		}
		last = at
	}
}

func TestOnceUsesLineURLElseHostGatewayDefault(t *testing.T) {
	for _, tc := range []struct{ lineURL, want string }{
		{"http://sweatshop-a-serve:7469", "http://sweatshop-a-serve:7469"},
		{"", "http://host.containers.internal:1"},
	} {
		bare, _ := seededRemote(t)
		now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
		e := &fakeEngine{}
		r := newRunner(t, bare, e, &now)
		r.Line.URL = tc.lineURL
		if ran, err := r.Once(context.Background()); err != nil || !ran {
			t.Fatalf("Once = %v, %v", ran, err)
		}
		if got := e.specs[0].Env["SWEATSHOP_LINE_URL"]; got != tc.want {
			t.Errorf("line url %q: SWEATSHOP_LINE_URL = %q, want %q", tc.lineURL, got, tc.want)
		}
	}
}
