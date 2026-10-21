package controller

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/station"
)

// fakeLine is serve and the engine in one: it holds the workloads serve
// wants and the engine objects, and records what the controller did.
type fakeLine struct {
	workloads []station.Workload
	objects   map[string]*Observed
	logs      map[string]string
	createErr error
	created   []Instance
	removed   []string
	reports   []station.WorkloadStatus
}

func newFakeLine(ws ...station.Workload) *fakeLine {
	return &fakeLine{workloads: ws, objects: map[string]*Observed{}, logs: map[string]string{}}
}

func (f *fakeLine) ControllerPending(context.Context) ([]station.Workload, error) {
	return f.workloads, nil
}

// ControllerReport applies the state change the way serve does.
func (f *fakeLine) ControllerReport(_ context.Context, rep station.WorkloadStatus) error {
	f.reports = append(f.reports, rep)
	for i := range f.workloads {
		if f.workloads[i].ID == rep.ID {
			f.workloads[i].State = rep.State
		}
	}
	return nil
}

func (f *fakeLine) Create(_ context.Context, in Instance) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, in)
	f.objects[in.Workload] = &Observed{Workload: in.Workload, Running: true}
	return nil
}

func (f *fakeLine) List(context.Context) ([]Observed, error) {
	var out []Observed
	for _, o := range f.objects {
		out = append(out, *o)
	}
	return out, nil
}

func (f *fakeLine) Logs(_ context.Context, id string) (string, error) { return f.logs[id], nil }

func (f *fakeLine) Remove(_ context.Context, id string) error {
	f.removed = append(f.removed, id)
	delete(f.objects, id)
	return nil
}

func (f *fakeLine) state(id string) string {
	for _, w := range f.workloads {
		if w.ID == id {
			return w.State
		}
	}
	return ""
}

func (f *fakeLine) lastReport(t *testing.T) station.WorkloadStatus {
	t.Helper()
	if len(f.reports) == 0 {
		t.Fatal("controller reported nothing")
	}
	return f.reports[len(f.reports)-1]
}

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time          { return c.now }
func (c *testClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newController(f *fakeLine) (*Controller, *testClock) {
	clock := &testClock{now: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}
	return &Controller{API: f, Engine: f, Grace: time.Minute, Now: clock.Now}, clock
}

func approved(id string) station.Workload {
	return station.Workload{ID: id, Station: "maker-1", State: station.WorkloadApproved,
		Spec: station.WorkloadSpec{Kind: "test", Image: "localhost/check@sha256:" + strings.Repeat("a", 64),
			Args: []string{"just", "ci"}, Env: map[string]string{"A": "1"},
			Resources: station.WorkloadResources{Memory: "1g", CPUs: "2", Pids: 100}}}
}

func once(t *testing.T, c *Controller) {
	t.Helper()
	if err := c.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestApprovedWorkloadIsCreatedOnceAndReportedRunning(t *testing.T) {
	f := newFakeLine(approved("wl-1"))
	c, _ := newController(f)
	once(t, c)
	once(t, c)
	want := Instance{Workload: "wl-1", Image: f.workloads[0].Spec.Image, Args: []string{"just", "ci"},
		Env: map[string]string{"A": "1"}, Memory: "1g", CPUs: "2", Pids: 100}
	if len(f.created) != 1 || f.created[0].Workload != want.Workload || f.created[0].Image != want.Image ||
		f.created[0].Memory != "1g" || f.created[0].CPUs != "2" || f.created[0].Pids != 100 || !slices.Equal(f.created[0].Args, want.Args) {
		t.Fatalf("created %+v, want exactly one %+v", f.created, want)
	}
	if got := f.state("wl-1"); got != station.WorkloadRunning {
		t.Fatalf("state %q, want running", got)
	}
}

func TestEngineCreateFailureIsReportedAndNotRetried(t *testing.T) {
	f := newFakeLine(approved("wl-1"))
	f.createErr = errors.New("no such image")
	c, _ := newController(f)
	if err := c.Once(context.Background()); err == nil {
		t.Fatal("create failure was swallowed")
	}
	if rep := f.lastReport(t); rep.State != station.WorkloadFailed || !strings.Contains(rep.Detail, "no such image") {
		t.Fatalf("report %+v, want failed with the engine's reason", rep)
	}
	f.createErr = nil
	once(t, c)
	if len(f.created) != 0 {
		t.Fatalf("a failed workload was started again: %+v", f.created)
	}
}

func TestUnsupportedRequestFailsInsteadOfRunningWithoutIt(t *testing.T) {
	w := approved("wl-1")
	w.Spec.Volumes = []string{"line-remote"}
	f := newFakeLine(w)
	c, _ := newController(f)
	once(t, c)
	if rep := f.lastReport(t); rep.State != station.WorkloadFailed || !strings.Contains(rep.Detail, "volumes") || len(f.created) != 0 {
		t.Fatalf("report %+v, created %+v; want failed, nothing started", rep, f.created)
	}
}

func TestExitedWorkloadReportsLogsThenIsRemovedAfterGrace(t *testing.T) {
	for _, tc := range []struct {
		code  int
		state string
	}{{0, station.WorkloadExited}, {3, station.WorkloadFailed}} {
		f := newFakeLine(approved("wl-1"))
		c, clock := newController(f)
		once(t, c)
		f.objects["wl-1"].Running, f.objects["wl-1"].ExitCode, f.objects["wl-1"].Since = false, tc.code, clock.now
		f.logs["wl-1"] = "all done"
		once(t, c)
		if rep := f.lastReport(t); rep.State != tc.state || rep.Logs != "all done" || !strings.Contains(rep.Detail, "exit code") {
			t.Fatalf("exit %d: report %+v, want %s with logs", tc.code, rep, tc.state)
		}
		once(t, c)
		if len(f.removed) != 0 {
			t.Fatalf("exit %d: object removed inside the grace period", tc.code)
		}
		clock.Advance(2 * time.Minute)
		once(t, c)
		if !slices.Equal(f.removed, []string{"wl-1"}) || f.state("wl-1") != station.WorkloadRemoved {
			t.Fatalf("exit %d: removed %v, state %q; want wl-1 removed after grace", tc.code, f.removed, f.state("wl-1"))
		}
	}
}

func TestStoppedAndExpiredWorkloadsAreRemovedWithTheirLogs(t *testing.T) {
	for name, mutate := range map[string]func(*station.Workload){
		"stopped": func(w *station.Workload) { w.Stopped = true },
		"expired": func(w *station.Workload) { w.State = station.WorkloadExpired },
	} {
		f := newFakeLine(approved("wl-1"))
		c, _ := newController(f)
		once(t, c)
		f.logs["wl-1"] = "partial"
		mutate(&f.workloads[0])
		once(t, c)
		rep := f.lastReport(t)
		if !slices.Equal(f.removed, []string{"wl-1"}) || rep.State != station.WorkloadRemoved || rep.Logs != "partial" {
			t.Fatalf("%s: removed %v, report %+v; want removed with logs", name, f.removed, rep)
		}
	}
}

func TestStoppedWorkloadWithoutObjectIsStillReportedRemoved(t *testing.T) {
	w := approved("wl-1")
	w.Stopped = true
	f := newFakeLine(w)
	c, _ := newController(f)
	once(t, c)
	if f.state("wl-1") != station.WorkloadRemoved || len(f.created) != 0 {
		t.Fatalf("state %q, created %+v; want removed without starting", f.state("wl-1"), f.created)
	}
}

func TestOrphanObjectsAreRemovedOnlyAfterGrace(t *testing.T) {
	f := newFakeLine()
	c, clock := newController(f)
	f.objects["wl-9"] = &Observed{Workload: "wl-9", Running: true, Since: clock.now}
	f.objects[""] = &Observed{Since: clock.now.Add(-time.Hour)}
	once(t, c)
	if len(f.removed) != 0 {
		t.Fatalf("removed %v: a fresh orphan, or an object with no workload label, must stay", f.removed)
	}
	clock.Advance(2 * time.Minute)
	once(t, c)
	if !slices.Equal(f.removed, []string{"wl-9"}) {
		t.Fatalf("removed %v, want only the labelled orphan wl-9", f.removed)
	}
}
