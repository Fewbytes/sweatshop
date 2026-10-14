package heijunka

import (
	"context"
	"errors"
	"path/filepath"
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

func TestLevelsFileRoundTrip(t *testing.T) {
	f := LevelsFile{Path: filepath.Join(t.TempDir(), "heijunka.json")}
	if l, err := f.Read(); err != nil || l != (Levels{}) {
		t.Fatalf("missing file: %+v, %v", l, err)
	}
	want := Levels{Agents: 3, WIPLimit: 3}
	if err := f.Apply(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if got, err := f.Read(); err != nil || got != want {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
}

func TestStepPublishesBalancedLevelsFromBacklog(t *testing.T) {
	levels := LevelsFile{Path: filepath.Join(t.TempDir(), "heijunka.json")}
	obs := &fakeObserver{snap: Snapshot{Ready: 5, InProgress: 1, Current: Levels{1, 1}}}
	w := &Worker{Observer: obs, Actuator: levels, Policy: DefaultPolicy}
	got, err := w.Step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Levels{Agents: 2, WIPLimit: 2} // backlog 6 wants 3 agents; one step per wake
	if got != want {
		t.Fatalf("Step returned %+v, want %+v", got, want)
	}
	if published, err := levels.Read(); err != nil || published != want {
		t.Fatalf("published %+v, %v; want %+v", published, err, want)
	}
}
