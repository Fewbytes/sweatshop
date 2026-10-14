// Package heijunka is the host-side worker that levels the line's flow. It is
// a plain loop, like the andon watcher: it wakes on a timer or when poked,
// looks at the line's backlog, and moves the agent count and the kanban WIP
// limit toward what the backlog needs, a bounded step at a time. See
// design-docs/heijunka.md for why it polls state instead of subscribing to
// events.
package heijunka

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Fewbytes/sweatshop/line/maker"
	"github.com/Fewbytes/sweatshop/line/station"
)

// Levels is the capacity the worker controls.
type Levels struct {
	Agents   int `json:"agents"`    // maker stations that should be running
	WIPLimit int `json:"wip_limit"` // cards allowed in progress at once
}

// Snapshot is what the worker sees of the line at one wake.
type Snapshot struct {
	Ready       int  // open cards no one has claimed
	InProgress  int  // claimed cards
	Stopped     bool // the andon cord is pulled
	MakersAlive int  // maker stations that heartbeated recently
	Current     Levels

	Now    time.Time
	Master string // line master's commit
	// KaizenIdle is true when a kaizen station heartbeated recently and is
	// not running; KaizenLastRun is when its last run began, zero if never.
	KaizenIdle    bool
	KaizenLastRun time.Time
	// KaizenRequest is the line's latest request for a Kaizen run, nil if none.
	KaizenRequest *station.KaizenRequest
}

// Policy bounds and shapes the levels. Zero values are invalid; use
// DefaultPolicy.
type Policy struct {
	MinAgents, MaxAgents int
	CardsPerAgent        int           // backlog one agent is expected to absorb
	WIPPerAgent          int           // kanban slots each agent gets
	MaxStep              int           // most agents added or removed per wake
	KaizenMinGap         time.Duration // least time from one Kaizen run's start to the next request
}

// DefaultPolicy keeps one agent warm, caps at four, and moves one at a time.
var DefaultPolicy = Policy{MinAgents: 1, MaxAgents: 4, CardsPerAgent: 2, WIPPerAgent: 1, MaxStep: 1, KaizenMinGap: 6 * time.Hour}

// Validate rejects policies that would divide by zero or never converge.
func (p Policy) Validate() error {
	if p.MinAgents < 0 || p.MaxAgents < p.MinAgents || p.CardsPerAgent < 1 || p.WIPPerAgent < 1 || p.MaxStep < 1 || p.KaizenMinGap < 0 {
		return fmt.Errorf("invalid heijunka policy %+v", p)
	}
	return nil
}

// Decide returns the levels the line should move to. It is pure.
//
//   - A stopped line takes no new work: WIP is held at one card (the fix) and
//     agents are left alone.
//   - Otherwise the target agent count covers the backlog (ready plus in
//     progress) at CardsPerAgent each, within [MinAgents, MaxAgents]; an empty
//     backlog therefore falls back to MinAgents.
//   - Agents move at most MaxStep per wake, so a burst of cards does not flap
//     the fleet; the WIP limit follows the agent count.
func Decide(s Snapshot, p Policy) Levels {
	if s.Stopped {
		return Levels{Agents: s.Current.Agents, WIPLimit: 1}
	}
	backlog := s.Ready + s.InProgress
	target := (backlog + p.CardsPerAgent - 1) / p.CardsPerAgent
	target = min(max(target, p.MinAgents), p.MaxAgents)
	agents := s.Current.Agents
	switch {
	case target > agents:
		agents = min(target, agents+p.MaxStep)
	case target < agents:
		agents = max(target, agents-p.MaxStep)
	}
	return Levels{Agents: agents, WIPLimit: agents * p.WIPPerAgent}
}

// ShouldRequestKaizen reports whether to ask Kaizen for a run now. It is pure.
// Heijunka only asks; Kaizen keeps its own guards and may decline. It asks
// when the line has spare capacity and Kaizen has had no request on this
// master:
//
//   - the cord is not pulled;
//   - the backlog is empty and a maker is idle, so a run costs no card its maker;
//   - a kaizen station is idle, with no request pending, and its last run began
//     at least KaizenMinGap ago;
//   - the latest request, answered either way, was made on an older master. A
//     declined request is therefore not repeated until master moves.
func ShouldRequestKaizen(s Snapshot, p Policy) bool {
	switch {
	case s.Stopped, !s.KaizenIdle:
		return false
	case s.Ready > 0 || s.InProgress >= s.MakersAlive:
		return false
	case !s.KaizenLastRun.IsZero() && s.Now.Sub(s.KaizenLastRun) < p.KaizenMinGap:
		return false
	case s.KaizenRequest != nil && (s.KaizenRequest.State == station.KaizenRequestPending || s.KaizenRequest.Master == s.Master):
		return false
	}
	return true
}

// Observer reads the line.
type Observer interface {
	Observe(ctx context.Context) (Snapshot, error)
}

// Actuator applies levels.
type Actuator interface {
	Apply(ctx context.Context, l Levels) error
}

// KaizenRequester asks the line for a Kaizen run.
type KaizenRequester interface {
	RequestKaizen(ctx context.Context) error
}

// Worker is one heijunka loop.
type Worker struct {
	Observer Observer
	Actuator Actuator
	Policy   Policy
	Interval time.Duration // periodic wake, default 5m
	Poke     <-chan struct{}
	Needs    *NeedsTriager   // nil leaves stale needs:<role> cards alone
	Kaizen   KaizenRequester // nil never requests a Kaizen run
	Log      *log.Logger
}

// Step observes once and applies new levels when they differ. It returns the
// levels now in force.
func (w *Worker) Step(ctx context.Context) (Levels, error) {
	if w.Needs != nil {
		if err := w.Needs.Triage(ctx, time.Now()); err != nil && w.Log != nil {
			w.Log.Printf("needs: %v", err)
		}
	}
	snap, err := w.Observer.Observe(ctx)
	if err != nil {
		return Levels{}, fmt.Errorf("observe: %w", err)
	}
	if w.Kaizen != nil && ShouldRequestKaizen(snap, w.Policy) {
		if err := w.Kaizen.RequestKaizen(ctx); err != nil {
			if w.Log != nil {
				w.Log.Printf("kaizen request: %v", err)
			}
		} else if w.Log != nil {
			w.Log.Printf("requested a kaizen run on master %s", snap.Master)
		}
	}
	want := Decide(snap, w.Policy)
	if want == snap.Current {
		return want, nil
	}
	if err := w.Actuator.Apply(ctx, want); err != nil {
		return snap.Current, fmt.Errorf("apply %+v: %w", want, err)
	}
	if w.Log != nil {
		w.Log.Printf("ready=%d in_progress=%d stopped=%t makers_alive=%d: %+v -> %+v",
			snap.Ready, snap.InProgress, snap.Stopped, snap.MakersAlive, snap.Current, want)
	}
	return want, nil
}

// Run steps on start, then on every Interval tick or Poke, until ctx is done.
// Step errors are logged and retried on the next wake.
func (w *Worker) Run(ctx context.Context) error {
	if err := w.Policy.Validate(); err != nil {
		return err
	}
	interval := w.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := w.Step(ctx); err != nil && ctx.Err() == nil && w.Log != nil {
			w.Log.Printf("step: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		case <-w.Poke:
		}
	}
}

// StatusAPI is the slice of the station client the observer needs.
type StatusAPI interface {
	Status(ctx context.Context) (station.Status, error)
}

// BeadsObserver counts cards with bd and the andon state from the station.
type BeadsObserver struct {
	API    StatusAPI
	Dir    string     // beads database directory
	Label  string     // label of cards on the line
	Levels LevelsFile // where the current levels are published
}

// Observe implements Observer.
func (b BeadsObserver) Observe(ctx context.Context) (Snapshot, error) {
	st, err := b.API.Status(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("status: %w", err)
	}
	ready, err := maker.BeadsCLI{Dir: b.Dir}.Ready(ctx, b.Label)
	if err != nil {
		return Snapshot{}, err
	}
	cmd := exec.CommandContext(ctx, "bd", "list", "--label", b.Label, "--status", "in_progress", "--limit", "0", "--json")
	cmd.Dir = b.Dir
	out, err := cmd.Output()
	if err != nil {
		return Snapshot{}, fmt.Errorf("bd list: %w", err)
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(out, &rows); err != nil {
		return Snapshot{}, fmt.Errorf("bd list: parse output: %w", err)
	}
	cur, err := b.Levels.Read()
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{
		Ready: len(ready), InProgress: len(rows), Stopped: st.Event != "", MakersAlive: st.MakersAlive(), Current: cur,
		Now: time.Now(), Master: st.Master, KaizenRequest: st.KaizenRequest,
	}
	for _, s := range st.Stations {
		if s.Role == "kaizen" {
			snap.KaizenIdle = s.Alive && s.State == station.StateIdle
			if s.LastRun != nil {
				snap.KaizenLastRun = *s.LastRun
			}
			break
		}
	}
	return snap, nil
}

// LevelsFile publishes the levels as JSON for the actuators that run makers
// (supervisor, deployer) to read. A missing file means no levels set yet.
type LevelsFile struct{ Path string }

// Read returns the published levels, or zero Levels when none were published.
func (f LevelsFile) Read() (Levels, error) {
	data, err := os.ReadFile(f.Path)
	if os.IsNotExist(err) {
		return Levels{}, nil
	}
	if err != nil {
		return Levels{}, err
	}
	var l Levels
	if err := json.Unmarshal(data, &l); err != nil {
		return Levels{}, fmt.Errorf("%s: %w", f.Path, err)
	}
	return l, nil
}

// Apply implements Actuator: it replaces the file atomically.
func (f LevelsFile) Apply(_ context.Context, l Levels) error {
	data, err := json.Marshal(l)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.Path), ".heijunka-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.Path)
}
