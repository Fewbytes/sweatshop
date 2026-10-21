package runner

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/station"
)

const remoteImage = "localhost/check@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// fakeServe stands in for serve's workload API. The workload moves to the next
// state in script each time Wait sleeps, which the test's After drives.
type fakeServe struct {
	deny     error
	requests []station.WorkloadSpec
	stopped  []string
	logs     string
	script   []station.Workload // states after approval, in order
	w        station.Workload
}

func (f *fakeServe) WorkloadRequest(_ context.Context, spec station.WorkloadSpec) (station.Workload, error) {
	if f.deny != nil {
		return station.Workload{}, f.deny
	}
	f.requests = append(f.requests, spec)
	f.w = station.Workload{ID: "wl-1", Spec: spec, State: station.WorkloadPending}
	return f.w, nil
}

func (f *fakeServe) WorkloadGet(context.Context, string) (station.Workload, error) { return f.w, nil }

func (f *fakeServe) WorkloadStop(_ context.Context, id string) (station.Workload, error) {
	f.stopped = append(f.stopped, id)
	return f.w, nil
}

func (f *fakeServe) WorkloadLogs(context.Context, string) (string, error) { return f.logs, nil }

// after advances the workload one step and fires at once.
func (f *fakeServe) after(time.Duration) <-chan time.Time {
	f.w = f.script[0]
	f.w.ID = "wl-1"
	f.script = f.script[1:]
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch
}

func newRemote(f *fakeServe) *Remote {
	return &Remote{API: f, Kind: "test", After: f.after}
}

func TestRemoteRunsContainerThroughWorkloadRequest(t *testing.T) {
	f := &fakeServe{logs: "ok\n", script: []station.Workload{
		{State: station.WorkloadRunning},
		{State: station.WorkloadFailed, Detail: station.WorkloadExitDetail(3)},
	}}
	r := newRemote(f)
	ctx := context.Background()
	err := r.Start(ctx, Spec{
		Name: "chk", Image: remoteImage, Args: []string{"just", "ci"}, Env: map[string]string{"A": "1"},
		Mounts: []Mount{{Volume: VolumeLineRemote, Target: "/line/remote.git", ReadOnly: true}},
		Memory: "2g", CPUs: "1", Pids: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := f.requests[0]
	if got.Kind != "test" || got.Image != remoteImage || strings.Join(got.Args, " ") != "just ci" ||
		got.Env["A"] != "1" || strings.Join(got.Volumes, ",") != VolumeLineRemote ||
		got.Resources != (station.WorkloadResources{Memory: "2g", CPUs: "1", Pids: 100}) {
		t.Errorf("request = %+v", got)
	}
	if st, _ := r.Inspect(ctx, "chk"); !st.Exists || !st.Running {
		t.Errorf("pending workload inspects as %+v, want existing and running", st)
	}
	code, err := r.Wait(ctx, "chk")
	if err != nil || code != 3 {
		t.Fatalf("Wait = %d, %v; want exit code 3", code, err)
	}
	if st, _ := r.Inspect(ctx, "chk"); !st.Exists || st.Running || st.ExitCode != 3 {
		t.Errorf("finished workload inspects as %+v, want exited with 3", st)
	}
	var out bytes.Buffer
	if err := r.Logs(ctx, "chk", &out); err != nil || out.String() != "ok\n" {
		t.Errorf("Logs = %q, %v", out.String(), err)
	}
	if err := r.Remove(ctx, "chk"); err != nil || len(f.stopped) != 1 || f.stopped[0] != "wl-1" {
		t.Errorf("Remove: err %v, stopped %v", err, f.stopped)
	}
	if st, _ := r.Inspect(ctx, "chk"); st.Exists {
		t.Errorf("removed container still inspects as %+v", st)
	}
}

func TestRemoteWaitReportsWorkloadsThatEndedWithoutAnExitCode(t *testing.T) {
	for _, w := range []station.Workload{
		{State: station.WorkloadFailed, Detail: "create: no such image"},
		{State: station.WorkloadExpired, Detail: "ttl"},
		{State: station.WorkloadRemoved, Detail: "stopped"},
	} {
		f := &fakeServe{script: []station.Workload{w}}
		r := newRemote(f)
		if err := r.Start(context.Background(), Spec{Name: "chk", Image: remoteImage}); err != nil {
			t.Fatal(err)
		}
		_, err := r.Wait(context.Background(), "chk")
		if err == nil || !strings.Contains(err.Error(), w.Detail) {
			t.Errorf("state %s: Wait err = %v, want one carrying %q", w.State, err, w.Detail)
		}
	}
}

func TestRemoteRefusesWhatTheRequestCannotCarry(t *testing.T) {
	denied := errors.New("workload wl-2 denied: no rule")
	cases := map[string]struct {
		serve *fakeServe
		spec  Spec
		want  string
	}{
		"secrets":        {&fakeServe{}, Spec{Name: "c", Image: remoteImage, Secrets: map[string]string{"TOKEN": "s3cret"}}, "secret"},
		"writable mount": {&fakeServe{}, Spec{Name: "c", Image: remoteImage, Mounts: []Mount{{Volume: "v", Target: "/t"}}}, "read-only"},
		"policy denial":  {&fakeServe{deny: denied}, Spec{Name: "c", Image: remoteImage}, "denied"},
	}
	for name, tc := range cases {
		err := newRemote(tc.serve).Start(context.Background(), tc.spec)
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: Start err = %v, want one mentioning %q and no secret value", name, err, tc.want)
		}
	}
	r := newRemote(&fakeServe{})
	if _, err := r.Pull(context.Background(), "localhost/check:latest"); err == nil {
		t.Error("Pull accepted a tag that is not pinned by digest")
	}
	if ref, err := r.Pull(context.Background(), remoteImage); err != nil || ref != remoteImage {
		t.Errorf("Pull of a pinned reference = %q, %v", ref, err)
	}
}
