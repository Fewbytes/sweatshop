package heijunka

import (
	"context"
	"errors"
	"github.com/Fewbytes/sweatshop/line/linestore"
	"github.com/Fewbytes/sweatshop/line/station"
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	p := DefaultPolicy
	tests := []struct {
		name string
		s    Snapshot
		want Levels
	}{
		{"no pending cards falls toward the minimum", Snapshot{Current: Levels{3, 3}}, Levels{2, 2}},
		{"idle at the minimum stays", Snapshot{Current: Levels{1, 1}}, Levels{1, 1}},
		{"large backlog grows one step", Snapshot{Ready: 20, Current: Levels{1, 1}}, Levels{2, 2}},
		{"large backlog stops at the cap", Snapshot{Ready: 20, Current: Levels{4, 4}}, Levels{4, 4}},
		{"in-progress cards count as backlog", Snapshot{InProgress: 6, Current: Levels{1, 1}}, Levels{2, 2}},
		{"first wake from nothing reaches the minimum", Snapshot{}, Levels{1, 1}},
		{"stopped line holds agents and limits WIP to the fix", Snapshot{Ready: 20, Stopped: true, Current: Levels{3, 3}}, Levels{3, 1}},
	}
	for _, tt := range tests {
		if got := Decide(tt.s, p); got != tt.want {
			t.Errorf("%s: got %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestPolicyValidate(t *testing.T) {
	if err := DefaultPolicy.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := DefaultPolicy
	bad.CardsPerAgent = 0
	if bad.Validate() == nil {
		t.Fatal("zero CardsPerAgent accepted")
	}
}

type fakeObserver struct {
	snap Snapshot
	err  error
}

func (f *fakeObserver) Observe(context.Context) (Snapshot, error) { return f.snap, f.err }

type fakeActuator struct {
	applied []Levels
	err     error
}

func (f *fakeActuator) Apply(_ context.Context, l Levels) error {
	f.applied = append(f.applied, l)
	return f.err
}

func TestStepAppliesOnlyChanges(t *testing.T) {
	obs := &fakeObserver{snap: Snapshot{Ready: 20, Current: Levels{1, 1}}}
	act := &fakeActuator{}
	w := &Worker{Observer: obs, Actuator: act, Policy: DefaultPolicy}
	if _, err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	obs.snap.Current = Levels{4, 4}
	if _, err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(act.applied) != 1 || act.applied[0] != (Levels{2, 2}) {
		t.Fatalf("applied %+v, want only {2 2}", act.applied)
	}
}

func TestStepErrors(t *testing.T) {
	boom := errors.New("boom")
	w := &Worker{Observer: &fakeObserver{err: boom}, Actuator: &fakeActuator{}, Policy: DefaultPolicy}
	if _, err := w.Step(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("observe error not returned: %v", err)
	}
	w = &Worker{Observer: &fakeObserver{snap: Snapshot{Ready: 9}}, Actuator: &fakeActuator{err: boom}, Policy: DefaultPolicy}
	if _, err := w.Step(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("apply error not returned: %v", err)
	}
}

func TestRunWakesOnPoke(t *testing.T) {
	obs := &fakeObserver{snap: Snapshot{Ready: 20, Current: Levels{1, 1}}}
	act := &fakeActuator{}
	poke := make(chan struct{})
	w := &Worker{Observer: obs, Actuator: act, Policy: DefaultPolicy, Interval: time.Hour, Poke: poke}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- w.Run(ctx) }()
	poke <- struct{}{} // first Step ran before the select accepted this
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(act.applied) != 2 {
		t.Fatalf("want a start step and a poked step, got %d applies", len(act.applied))
	}
}

type fakeLevelsAPI struct{ published []linestore.Levels }

func (f *fakeLevelsAPI) PublishHeijunkaLevels(_ context.Context, l linestore.Levels) error {
	f.published = append(f.published, l)
	return nil
}

func TestStepPublishesBalancedLevelsFromBacklog(t *testing.T) {
	levels := &fakeLevelsAPI{}
	obs := &fakeObserver{snap: Snapshot{Ready: 5, InProgress: 1, Current: Levels{1, 1}}}
	w := &Worker{Observer: obs, Actuator: LevelsPublisher{API: levels}, Policy: DefaultPolicy}
	got, err := w.Step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Levels{Agents: 2, WIPLimit: 2} // backlog 6 wants 3 agents; one step per wake
	if got != want {
		t.Fatalf("Step returned %+v, want %+v", got, want)
	}
	if len(levels.published) != 1 || Levels(levels.published[0]) != want {
		t.Fatalf("published %+v; want [%+v]", levels.published, want)
	}
}

// idleLine is a line with spare capacity: no backlog, one idle maker, an idle
// kaizen station that has not run for a day.
func idleLine() Snapshot {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	return Snapshot{MakersAlive: 1, Now: now, Master: "m2", KaizenIdle: true, KaizenLastRun: now.Add(-24 * time.Hour)}
}

func TestShouldRequestKaizen(t *testing.T) {
	p := DefaultPolicy
	for _, tc := range []struct {
		name   string
		mutate func(*Snapshot)
		want   bool
	}{
		{"spare capacity", func(*Snapshot) {}, true},
		{"kaizen never ran", func(s *Snapshot) { s.KaizenLastRun = time.Time{} }, true},
		{"previous request answered on an older master", func(s *Snapshot) {
			s.KaizenRequest = &station.KaizenRequest{State: station.KaizenRequestDeclined, Master: "m1"}
		}, true},
		{"cord pulled", func(s *Snapshot) { s.Stopped = true }, false},
		{"cards ready", func(s *Snapshot) { s.Ready = 1 }, false},
		{"every maker busy", func(s *Snapshot) { s.InProgress = 1 }, false},
		{"no maker alive", func(s *Snapshot) { s.MakersAlive = 0 }, false},
		{"kaizen busy or silent", func(s *Snapshot) { s.KaizenIdle = false }, false},
		{"kaizen ran inside the gap", func(s *Snapshot) { s.KaizenLastRun = s.Now.Add(-time.Hour) }, false},
		{"request pending", func(s *Snapshot) {
			s.KaizenRequest = &station.KaizenRequest{State: station.KaizenRequestPending, Master: "m1"}
		}, false},
		{"declined on this master", func(s *Snapshot) {
			s.KaizenRequest = &station.KaizenRequest{State: station.KaizenRequestDeclined, Master: "m2", Reason: "master has not moved since the last run"}
		}, false},
	} {
		s := idleLine()
		tc.mutate(&s)
		if got := ShouldRequestKaizen(s, p); got != tc.want {
			t.Errorf("%s: got %t, want %t", tc.name, got, tc.want)
		}
	}
}

type fakeKaizen struct{ requests int }

func (f *fakeKaizen) RequestKaizen(context.Context) error { f.requests++; return nil }

func TestStepRequestsKaizenOnlyWhenTheLineHasCapacity(t *testing.T) {
	obs := &fakeObserver{snap: idleLine()}
	kz := &fakeKaizen{}
	w := &Worker{Observer: obs, Actuator: &fakeActuator{}, Policy: DefaultPolicy, Kaizen: kz}
	if _, err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	obs.snap.Ready = 3
	if _, err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if kz.requests != 1 {
		t.Fatalf("got %d requests, want 1", kz.requests)
	}
}
