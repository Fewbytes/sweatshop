package station

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/constitution"
)

const workloadImage = "localhost/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type workloadRig struct {
	srv                 *Server
	maker, other, gemba Client
	controller          Client
	ch                  *Channel
	fd                  *fakeDiscordServer
	now                 time.Time
}

func newWorkloadRig(t *testing.T) *workloadRig {
	t.Helper()
	srv, _, _, ch, fd := consRig(t)
	r := &workloadRig{srv: srv, ch: ch, fd: fd, now: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)}
	srv.Stations = append(srv.Stations, &Station{Name: "m2", Role: "maker", Token: "m2t"}, &Station{Name: "g", Role: "gemba", Token: "gt"})
	srv.Guard.Constitution.Workloads = constitution.Workloads{
		Images: []string{"localhost/*"},
		Rules: []constitution.WorkloadRule{
			{Roles: []string{"maker", "gemba"}, Kinds: []string{"test"}, Approval: "auto", MaxPerStation: 2, Memory: "4g", CPUs: "2", Pids: 1024, TTL: "2h"},
			{Roles: []string{"gemba"}, Kinds: []string{"staging"}, Approval: "king", Expose: true, TTL: "72h"},
		},
	}
	srv.Workloads = &WorkloadStore{Path: filepath.Join(t.TempDir(), "workloads.json"), Now: func() time.Time { return r.now }}
	srv.ControllerToken = "ctl-secret"
	ch.Workload = srv.ApproveWorkload
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	r.maker = Client{URL: ts.URL, Token: "mt"}
	r.other = Client{URL: ts.URL, Token: "m2t"}
	r.gemba = Client{URL: ts.URL, Token: "gt"}
	r.controller = Client{URL: ts.URL, Token: "ctl-secret"}
	return r
}

func (r *workloadRig) pending(t *testing.T) []Workload {
	t.Helper()
	var out []Workload
	if _, err := r.controller.do(context.Background(), "GET", "/v1/controller/pending", nil, nil, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (r *workloadRig) report(t *testing.T, id, state, logs string) {
	t.Helper()
	body := `{"id":"` + id + `","state":"` + state + `","logs":"` + logs + `"}`
	if _, err := r.controller.do(context.Background(), "POST", "/v1/controller/status", nil, strings.NewReader(body), nil); err != nil {
		t.Fatal(err)
	}
}

func (r *workloadRig) king(t *testing.T, id, author, text string) {
	t.Helper()
	r.fd.add(id, author, text)
	if err := r.ch.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func testSpec() WorkloadSpec {
	return WorkloadSpec{Kind: "test", Image: workloadImage, Purpose: "run the check"}
}

func TestAutoApprovedWorkloadRunsExpiresAndIsRemoved(t *testing.T) {
	r := newWorkloadRig(t)
	ctx := context.Background()

	w, err := r.maker.WorkloadRequest(ctx, testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if w.ID != "wl-1" || w.State != WorkloadApproved || w.Station != "m" || w.Role != "maker" || w.Rule != "maker/test" {
		t.Fatalf("request: %+v", w)
	}
	if w.Spec.Resources != (WorkloadResources{Memory: "4g", CPUs: "2", Pids: 1024}) || w.Spec.TTL != "2h0m0s" || !w.ExpiresAt.Equal(r.now.Add(2*time.Hour)) {
		t.Fatalf("rule bounds not applied: %+v expires %v", w.Spec, w.ExpiresAt)
	}
	if got := r.pending(t); len(got) != 1 || got[0].ID != "wl-1" {
		t.Fatalf("controller pending: %+v", got)
	}

	r.report(t, "wl-1", WorkloadRunning, "hello from the container")
	got, err := r.maker.WorkloadGet(ctx, "wl-1")
	if err != nil || got.State != WorkloadRunning {
		t.Fatalf("get: %+v %v", got, err)
	}
	if logs, err := r.maker.WorkloadLogs(ctx, "wl-1"); err != nil || logs != "hello from the container" {
		t.Fatalf("logs %q %v", logs, err)
	}

	r.now = r.now.Add(2 * time.Hour)
	got, _ = r.maker.WorkloadGet(ctx, "wl-1")
	if got.State != WorkloadExpired {
		t.Fatalf("after ttl: %s", got.State)
	}
	if p := r.pending(t); len(p) != 1 || p[0].State != WorkloadExpired {
		t.Fatalf("expired workload must stay visible to the controller until removed: %+v", p)
	}
	r.report(t, "wl-1", WorkloadRemoved, "")
	if p := r.pending(t); len(p) != 0 {
		t.Fatalf("removed workload still pending: %+v", p)
	}
	got, _ = r.maker.WorkloadGet(ctx, "wl-1")
	var states []string
	for _, e := range got.History {
		states = append(states, e.State+"/"+e.By)
	}
	if strings.Join(states, " ") != "pending/m approved/serve running/controller expired/serve removed/controller" {
		t.Fatalf("audit history: %v", states)
	}
}

func TestKingApprovesStagingWorkloadOnTheChannel(t *testing.T) {
	r := newWorkloadRig(t)
	ctx := context.Background()
	r.fd.add("1", "1", "history")
	_ = r.ch.Poll(ctx)

	spec := WorkloadSpec{Kind: "staging", Image: workloadImage, Purpose: "demo\nthe build", Expose: &WorkloadExpose{Port: 8080, Name: "demo"}}
	w, err := r.gemba.WorkloadRequest(ctx, spec)
	if err != nil || w.State != WorkloadPending {
		t.Fatalf("request: %+v %v", w, err)
	}
	if len(r.fd.posted) != 1 || !strings.Contains(r.fd.posted[0], "reply: approve workload wl-1") || strings.Contains(r.fd.posted[0], "\n") {
		t.Fatalf("channel post: %q", r.fd.posted)
	}
	if len(r.pending(t)) != 0 {
		t.Fatal("controller sees a request the king has not approved")
	}

	r.king(t, "2", "666", "approve workload wl-1") // not the king
	if got, _ := r.gemba.WorkloadGet(ctx, "wl-1"); got.State != WorkloadPending {
		t.Fatalf("non-king approval took effect: %s", got.State)
	}
	r.king(t, "3", kingID, "approve workload wl-1")
	got, _ := r.gemba.WorkloadGet(ctx, "wl-1")
	if got.State != WorkloadApproved || !got.ExpiresAt.Equal(r.now.Add(72*time.Hour)) {
		t.Fatalf("after king: %+v", got)
	}
	if last := got.History[len(got.History)-1]; last.By != "king:3" {
		t.Fatalf("approval not attributed to the king's message: %+v", last)
	}
	if len(r.pending(t)) != 1 {
		t.Fatal("approved workload not offered to the controller")
	}
}

func TestPolicyDeniesRequestsOutsideTheRulesAndRecordsThem(t *testing.T) {
	r := newWorkloadRig(t)
	ctx := context.Background()
	over := func(f func(*WorkloadSpec)) WorkloadSpec { s := testSpec(); f(&s); return s }
	cases := []struct {
		name string
		by   Client
		spec WorkloadSpec
		want string
	}{
		{"kind no rule covers for the role", r.maker, over(func(s *WorkloadSpec) { s.Kind = "staging" }), "403"},
		{"image outside the allowlist", r.maker, over(func(s *WorkloadSpec) { s.Image = "docker.io/evil@sha256:" + strings.Repeat("a", 64) }), "not in the allowed images"},
		{"memory above the rule", r.maker, over(func(s *WorkloadSpec) { s.Resources.Memory = "8g" }), "exceeds the rule's 4g"},
		{"ttl above the rule", r.maker, over(func(s *WorkloadSpec) { s.TTL = "3h" }), "exceeds the rule's"},
		{"expose the rule does not allow", r.maker, over(func(s *WorkloadSpec) { s.Expose = &WorkloadExpose{Port: 80, Name: "x"} }), "does not allow exposing"},
	}
	for _, c := range cases {
		if _, err := c.by.WorkloadRequest(ctx, c.spec); err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "403") {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	list, _ := r.maker.WorkloadList(ctx)
	if len(list) != len(cases) {
		t.Fatalf("denied requests must be recorded for the audit: %d", len(list))
	}
	for _, w := range list {
		if w.State != WorkloadDenied || w.Detail == "" {
			t.Errorf("record: %+v", w)
		}
	}
	if len(r.pending(t)) != 0 || len(r.fd.posted) != 0 {
		t.Fatal("denied request reached the controller or the channel")
	}
}

func TestWorkloadQuotaCountsLiveWorkloadsOfTheStation(t *testing.T) {
	r := newWorkloadRig(t)
	ctx := context.Background()
	var first Workload
	for i := range 2 {
		w, err := r.maker.WorkloadRequest(ctx, testSpec())
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = w
		}
	}
	if _, err := r.maker.WorkloadRequest(ctx, testSpec()); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("third request: %v", err)
	}
	if _, err := r.other.WorkloadRequest(ctx, testSpec()); err != nil {
		t.Fatalf("another station shares no quota: %v", err)
	}
	if _, err := r.maker.WorkloadStop(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.maker.WorkloadRequest(ctx, testSpec()); err != nil {
		t.Fatalf("a stopped workload still counts: %v", err)
	}
}

func TestMalformedWorkloadRequestsAreRefusedWithoutARecord(t *testing.T) {
	r := newWorkloadRig(t)
	ctx := context.Background()
	over := func(f func(*WorkloadSpec)) WorkloadSpec { s := testSpec(); f(&s); return s }
	for name, spec := range map[string]WorkloadSpec{
		"tag instead of digest": over(func(s *WorkloadSpec) { s.Image = "localhost/app:latest" }),
		"host path volume":      over(func(s *WorkloadSpec) { s.Volumes = []string{"/var/run:/x"} }),
		"image and build": over(func(s *WorkloadSpec) {
			s.Build = &WorkloadBuild{Commit: strings.Repeat("a", 40), Dockerfile: "Dockerfile"}
		}),
		"neither image nor build": over(func(s *WorkloadSpec) { s.Image = "" }),
		"escaping dockerfile":     {Kind: "test", Build: &WorkloadBuild{Commit: strings.Repeat("a", 40), Dockerfile: "../x"}},
		"bad env name":            over(func(s *WorkloadSpec) { s.Env = map[string]string{"A=B": "x"} }),
	} {
		if _, err := r.maker.WorkloadRequest(ctx, spec); err == nil || !strings.Contains(err.Error(), "400") {
			t.Errorf("%s: %v", name, err)
		}
	}
	body := `{"kind":"test","image":"` + workloadImage + `","station":"g","privileged":true}`
	if _, err := r.maker.do(ctx, "POST", "/v1/workloads", nil, strings.NewReader(body), nil); err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("unknown fields must be refused, not ignored: %v", err)
	}
	if list, _ := r.maker.WorkloadList(ctx); len(list) != 0 {
		t.Fatalf("malformed requests left records: %+v", list)
	}
}

func TestWorkloadAccessFollowsStationIdentity(t *testing.T) {
	r := newWorkloadRig(t)
	ctx := context.Background()
	w, err := r.maker.WorkloadRequest(ctx, testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.other.WorkloadGet(ctx, w.ID); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("another maker read it: %v", err)
	}
	if _, err := r.other.WorkloadLogs(ctx, w.ID); err == nil {
		t.Fatal("another maker read the logs")
	}
	if _, err := r.other.WorkloadStop(ctx, w.ID); err == nil {
		t.Fatal("another maker stopped it")
	}
	if list, _ := r.other.WorkloadList(ctx); len(list) != 0 {
		t.Fatalf("list leaks: %+v", list)
	}
	if list, _ := r.gemba.WorkloadList(ctx); len(list) != 1 {
		t.Fatalf("gemba sees all: %+v", list)
	}

	if _, err := r.maker.do(ctx, "GET", "/v1/controller/pending", nil, nil, nil); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("station on a controller route: %v", err)
	}
	if _, err := r.controller.do(ctx, "GET", "/v1/workloads", nil, nil, nil); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("controller token on a station route: %v", err)
	}
	if _, err := r.controller.do(ctx, "GET", "/v1/cards", nil, nil, nil); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("controller token reached cards: %v", err)
	}
}

func TestStopWorkloadWithdrawsPendingAndFlagsRunningForTheController(t *testing.T) {
	r := newWorkloadRig(t)
	ctx := context.Background()
	pend, _ := r.gemba.WorkloadRequest(ctx, WorkloadSpec{Kind: "staging", Image: workloadImage})
	if w, err := r.gemba.WorkloadStop(ctx, pend.ID); err != nil || w.State != WorkloadRemoved {
		t.Fatalf("withdraw: %+v %v", w, err)
	}
	if err := r.srv.ApproveWorkload(ctx, pend.ID, "9"); err == nil {
		t.Fatal("approved a withdrawn request")
	}

	run, _ := r.maker.WorkloadRequest(ctx, testSpec())
	r.report(t, run.ID, WorkloadRunning, "")
	if _, err := r.maker.WorkloadStop(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if p := r.pending(t); len(p) != 1 || !p[0].Stopped || p[0].State != WorkloadRunning {
		t.Fatalf("controller must be told to remove it: %+v", p)
	}
}

func TestControllerCannotReportStatesItDoesNotOwn(t *testing.T) {
	r := newWorkloadRig(t)
	ctx := context.Background()
	w, _ := r.maker.WorkloadRequest(ctx, testSpec())
	for _, state := range []string{WorkloadApproved, WorkloadDenied, WorkloadExpired, "bogus"} {
		body := `{"id":"` + w.ID + `","state":"` + state + `"}`
		if _, err := r.controller.do(ctx, "POST", "/v1/controller/status", nil, strings.NewReader(body), nil); err == nil || !strings.Contains(err.Error(), "400") {
			t.Errorf("state %s: %v", state, err)
		}
	}
	body := `{"id":"wl-99","state":"running"}`
	if _, err := r.controller.do(ctx, "POST", "/v1/controller/status", nil, strings.NewReader(body), nil); err == nil || !strings.Contains(err.Error(), "409") {
		t.Errorf("unknown workload: %v", err)
	}
}

func TestResolveWorkloadFillsAndBoundsResourcesByTheRule(t *testing.T) {
	rule := constitution.WorkloadRule{TTL: "2h", Memory: "4g", CPUs: "2", Pids: 100}
	unbounded := constitution.WorkloadRule{TTL: "2h"}
	withResources := func(r WorkloadResources) WorkloadSpec { return WorkloadSpec{Resources: r} }
	cases := []struct {
		name    string
		rule    constitution.WorkloadRule
		spec    WorkloadSpec
		want    WorkloadResources
		wantErr string
	}{
		{"empty request takes the rule's bounds", rule, WorkloadSpec{}, WorkloadResources{Memory: "4g", CPUs: "2", Pids: 100}, ""},
		{"request below the bounds is kept", rule, withResources(WorkloadResources{Memory: "512m", CPUs: "0.5", Pids: 10}), WorkloadResources{Memory: "512m", CPUs: "0.5", Pids: 10}, ""},
		{"request equal to the bounds is kept", rule, withResources(WorkloadResources{Memory: "4g", CPUs: "2", Pids: 100}), WorkloadResources{Memory: "4g", CPUs: "2", Pids: 100}, ""},
		{"memory above the rule", rule, withResources(WorkloadResources{Memory: "8g"}), WorkloadResources{}, "memory 8g exceeds the rule's 4g"},
		{"cpus above the rule", rule, withResources(WorkloadResources{CPUs: "3"}), WorkloadResources{}, "cpus 3 exceeds the rule's 2"},
		{"pids above the rule", rule, withResources(WorkloadResources{Pids: 101}), WorkloadResources{}, "pids 101 exceeds the rule's 100"},
		{"malformed memory under a bounded rule", rule, withResources(WorkloadResources{Memory: "lots"}), WorkloadResources{}, "want a size like 4g or 512m"},
		{"malformed cpus under a bounded rule", rule, withResources(WorkloadResources{CPUs: "many"}), WorkloadResources{}, "want a positive number"},
		{"malformed memory under an unbounded rule", unbounded, withResources(WorkloadResources{Memory: "lots"}), WorkloadResources{}, "want a size like 4g or 512m"},
		{"malformed cpus under an unbounded rule", unbounded, withResources(WorkloadResources{CPUs: "many"}), WorkloadResources{}, "want a positive number"},
		{"unbounded rule keeps any valid request", unbounded, withResources(WorkloadResources{Memory: "64g", CPUs: "64", Pids: 9999}), WorkloadResources{Memory: "64g", CPUs: "64", Pids: 9999}, ""},
		{"unbounded rule leaves resources empty", unbounded, WorkloadSpec{}, WorkloadResources{}, ""},
	}
	for _, c := range cases {
		got, err := resolveWorkload(c.rule, c.spec)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil || got.Resources != c.want {
			t.Errorf("%s: got %+v, %v; want %+v", c.name, got.Resources, err, c.want)
		}
	}
}
