package station

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func livenessRig(t *testing.T) (*Server, Client, Client) {
	t.Helper()
	srv := &Server{Stations: []*Station{
		{Name: "v", Role: "viceroy", Token: "vt"},
		{Name: "m1", Role: "maker", Token: "m1t"},
		{Name: "m2", Role: "maker", Token: "m2t"},
	}}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, Client{URL: ts.URL, Token: "m1t"}, Client{URL: ts.URL, Token: "vt"}
}

func TestHeartbeatShowsInStatusStations(t *testing.T) {
	srv, m1, v := livenessRig(t)
	ctx := context.Background()
	if err := m1.Heartbeat(ctx, Heartbeat{State: StateWorking, Card: "x-1", Commit: "abc"}); err != nil {
		t.Fatal(err)
	}
	// Status reads master from a real repo; the stations come from the same
	// bookkeeping, so read the snapshot the route serves.
	got := srv.liveness.snapshot(srv.Stations, time.Now())
	if len(got) != 2 {
		t.Fatalf("want m1 and unseen m2 (not the viceroy), got %+v", got)
	}
	if got[0].Name != "m1" || got[0].State != StateWorking || got[0].Card != "x-1" || got[0].Commit != "abc" || !got[0].Alive || got[0].LastSeen == nil {
		t.Fatalf("m1: %+v", got[0])
	}
	if got[1].Name != "m2" || got[1].State != StateUnseen || got[1].Alive || got[1].LastSeen != nil {
		t.Fatalf("m2: %+v", got[1])
	}
	if err := v.Heartbeat(ctx, Heartbeat{State: StateIdle}); err != nil {
		t.Fatal(err)
	}
	if got := srv.liveness.snapshot(srv.Stations, time.Now()); len(got) != 3 || got[0].Name != "v" {
		t.Fatalf("a non-maker that heartbeats is listed: %+v", got)
	}
}

func TestHeartbeatRejectsBadState(t *testing.T) {
	_, m1, _ := livenessRig(t)
	for _, hb := range []Heartbeat{{State: "napping"}, {State: StateWorking}, {State: StateIdle, Card: "x-1"}} {
		if err := m1.Heartbeat(context.Background(), hb); err == nil || !strings.Contains(err.Error(), "400") {
			t.Fatalf("%+v: %v", hb, err)
		}
	}
}

func TestStationStatusExpiresAfterSilenceLimit(t *testing.T) {
	srv, _, _ := livenessRig(t)
	t0 := time.Now()
	srv.liveness.beat("m1", Heartbeat{State: StateIdle}, t0)
	if got := srv.liveness.snapshot(srv.Stations, t0.Add(MakerSilenceLimit)); !got[0].Alive {
		t.Fatalf("alive at the limit: %+v", got[0])
	}
	if got := srv.liveness.snapshot(srv.Stations, t0.Add(MakerSilenceLimit+time.Second)); got[0].Alive {
		t.Fatalf("alive past the limit: %+v", got[0])
	}
}

func TestStatusMakersAliveCountsOnlyLiveMakers(t *testing.T) {
	st := Status{Stations: []StationStatus{
		{Role: "maker", Alive: true}, {Role: "maker"}, {Role: "viceroy", Alive: true},
	}}
	if n := st.MakersAlive(); n != 1 {
		t.Fatalf("got %d", n)
	}
}

func TestMakerWatchAlertsOnceAndClears(t *testing.T) {
	srv, _, _ := livenessRig(t)
	var posts []string
	failNext := false
	w := &MakerWatch{Server: srv, Started: time.Now(), Announce: func(_ context.Context, text string) error {
		if failNext {
			failNext = false
			return errors.New("discord down")
		}
		posts = append(posts, text)
		return nil
	}}
	t0 := w.Started
	check := func(at time.Time) {
		t.Helper()
		if err := w.Check(context.Background(), at); err != nil {
			t.Fatal(err)
		}
	}

	check(t0.Add(MakerSilenceLimit)) // not yet silent long enough
	if len(posts) != 0 {
		t.Fatalf("early alert: %v", posts)
	}
	failNext = true
	if err := w.Check(context.Background(), t0.Add(MakerSilenceLimit+time.Minute)); err == nil {
		t.Fatal("failed post not reported")
	}
	check(t0.Add(MakerSilenceLimit + time.Minute)) // retried after the failure
	check(t0.Add(MakerSilenceLimit + 2*time.Minute))
	if len(posts) != 1 || posts[0] != "no maker alive since "+t0.UTC().Format(time.RFC3339) {
		t.Fatalf("want one alert, got %v", posts)
	}

	back := t0.Add(10 * time.Minute)
	srv.liveness.beat("m2", Heartbeat{State: StateIdle}, back)
	check(back)
	check(back.Add(time.Second))
	if len(posts) != 2 || posts[1] != "a maker is alive again" {
		t.Fatalf("want one all-clear, got %v", posts)
	}

	check(back.Add(MakerSilenceLimit + time.Minute)) // silent again: alerts again, since the last beat
	if len(posts) != 3 || posts[2] != "no maker alive since "+back.UTC().Format(time.RFC3339) {
		t.Fatalf("second alert: %v", posts)
	}
}

func TestStatusRouteIncludesStations(t *testing.T) {
	r := newRig(t)
	if err := r.maker.Heartbeat(r.ctx, Heartbeat{State: StateIdle, Commit: "abc"}); err != nil {
		t.Fatal(err)
	}
	st, err := r.andon.Status(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got *StationStatus
	for i := range st.Stations {
		if st.Stations[i].Role == makerRole {
			got = &st.Stations[i]
		}
	}
	if got == nil || got.State != StateIdle || got.Commit != "abc" || !got.Alive || got.LastSeen == nil {
		t.Fatalf("stations = %+v", st.Stations)
	}
}

func TestMakerWatchWarnsOnceOnClockSkewAndClears(t *testing.T) {
	srv, _, _ := livenessRig(t)
	var posts []string
	w := &MakerWatch{Server: srv, Started: time.Now(), Announce: func(_ context.Context, text string) error {
		posts = append(posts, text)
		return nil
	}}
	t0 := w.Started
	behind := func(at time.Time, by time.Duration) {
		t.Helper()
		clock := at.Add(-by)
		srv.liveness.beat("m1", Heartbeat{State: StateIdle, Clock: &clock}, at)
		if err := w.Check(context.Background(), at); err != nil {
			t.Fatal(err)
		}
	}

	behind(t0, MaxClockSkew) // at the limit: fine
	if len(posts) != 0 {
		t.Fatalf("early warning: %v", posts)
	}
	behind(t0.Add(time.Minute), 58*time.Minute)
	behind(t0.Add(2*time.Minute), 58*time.Minute)
	if len(posts) != 1 || !strings.Contains(posts[0], "m1") || !strings.Contains(posts[0], "-58m0s") || !strings.Contains(posts[0], "date -u -s @$(date +%s)") {
		t.Fatalf("want one warning, got %v", posts)
	}
	behind(t0.Add(3*time.Minute), 0)
	if len(posts) != 2 || !strings.Contains(posts[1], "back in step") {
		t.Fatalf("want one all-clear, got %v", posts)
	}
}

func TestClientHeartbeatCarriesStationClock(t *testing.T) {
	srv, m1, _ := livenessRig(t)
	before := time.Now()
	if err := m1.Heartbeat(context.Background(), Heartbeat{State: StateIdle}); err != nil {
		t.Fatal(err)
	}
	seen := srv.liveness.seen["m1"]
	if seen.Clock == nil || seen.Clock.Before(before) || seen.Clock.After(time.Now()) {
		t.Fatalf("clock %v not stamped between %v and now", seen.Clock, before)
	}
}
