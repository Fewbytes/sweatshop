// Package campaign is depmaint's state machine: bounded upgrade campaigns
// that plan, issue work orders, and learn from recorded attempt verdicts.
// It computes nothing about dependency graphs itself — the planner does —
// and performs no I/O beyond its Store; graph access arrives through World.
package campaign

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Fewbytes/sweatshop/depmaint/internal/planner"
)

// Type is the campaign kind. Remove campaigns (exploration, candidates) are
// not implemented yet.
type Type string

const (
	TypeUpgrade Type = "upgrade"
	TypeRuntime Type = "runtime" // an upgrade whose package is a runtime node
)

// Status is where a campaign is in its life.
type Status string

const (
	StatusPlanned   Status = "planned"
	StatusRunning   Status = "running"
	StatusBlocked   Status = "blocked" // infeasible plan, or the step's only attempt was rejected
	StatusSucceeded Status = "succeeded"
	StatusExhausted Status = "exhausted"
)

// Verdict is an attempt's outcome. Executors report the first three;
// VerdictSuperseded is assigned by Record to late reports.
type Verdict string

const (
	VerdictSucceeded  Verdict = "succeeded"
	VerdictRejected   Verdict = "rejected"
	VerdictFailed     Verdict = "failed"
	VerdictSuperseded Verdict = "superseded"
)

// Budget bounds a campaign; whichever limit is hit first exhausts it.
type Budget struct {
	MaxAttempts int
	MaxDuration time.Duration
}

// Campaign is one goal: move Package to Target within Budget.
type Campaign struct {
	ID      string
	Type    Type
	Package string // bare "ecosystem:name"
	Target  string
	Roots   []string // pinned at creation
	Status  Status
	Budget  Budget
	Spent   int // attempts recorded, racers and failures included
	// Completed counts steps landed by a succeeded verdict. The live step is
	// Completed+1 of Completed+len(Plan.Steps): each success re-plans, so
	// the plan only ever holds the steps still ahead.
	Completed  int
	Plan       *planner.Plan // nil until planned
	PlanHash   string
	SnapshotID string // snapshot the plan was computed on
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Attempt is one executor's report on one step. Rows are append-only.
type Attempt struct {
	ID             string
	CampaignID     string
	Step           int
	PlanHash       string
	SnapshotBefore string
	SnapshotAfter  string // set only for a live succeeded verdict
	Verdict        Verdict
	Reason         string
	Executor       string
	PRURL          string
	CommitSHA      string
	CreatedAt      time.Time
}

// World gives the engine the project graph for a campaign's pinned roots.
// refresh re-expands from the working tree (after an attempt landed);
// otherwise the latest cached snapshot is used. The returned id identifies
// the snapshot the graph came from.
type World interface {
	Graph(ctx context.Context, roots []string, refresh bool) (g planner.Graph, snapshotID string, err error)
}

// Engine drives campaigns. Now is injected so budgets are testable.
type Engine struct {
	Store    *Store
	World    World
	Registry planner.Registry
	Accepts  planner.Accepts
	Now      func() time.Time
}

// NewSpec is what `campaign new` asks for.
type NewSpec struct {
	Type    Type
	Package string
	Target  string
	Roots   []string
	Budget  Budget
}

// New creates an unplanned campaign with the next sequential id.
func (e Engine) New(ctx context.Context, spec NewSpec) (Campaign, error) {
	switch {
	case spec.Type != TypeUpgrade && spec.Type != TypeRuntime:
		return Campaign{}, fmt.Errorf("campaign type %q not supported (want upgrade or runtime)", spec.Type)
	case spec.Package == "" || spec.Target == "":
		return Campaign{}, errors.New("campaign needs a package and a target version")
	case len(spec.Roots) == 0:
		return Campaign{}, errors.New("campaign needs at least one root")
	case spec.Budget.MaxAttempts <= 0 || spec.Budget.MaxDuration <= 0:
		return Campaign{}, errors.New("campaign budget needs a positive attempt limit and duration")
	}
	n, err := e.Store.count(ctx, `SELECT COUNT(*) FROM campaign`)
	if err != nil {
		return Campaign{}, err
	}
	now := e.Now()
	c := Campaign{ID: fmt.Sprintf("dep_c%d", n+1), Type: spec.Type, Package: spec.Package, Target: spec.Target,
		Roots: spec.Roots, Status: StatusPlanned, Budget: spec.Budget, CreatedAt: now, UpdatedAt: now}
	return c, e.Store.insert(ctx, c)
}

// Plan (re)computes blockers and the upgrade plan on the current graph and
// stores it. A package already at its target completes the campaign; an
// infeasible plan blocks it.
func (e Engine) Plan(ctx context.Context, id string, refresh bool) (Campaign, error) {
	c, err := e.Store.Get(ctx, id)
	if err != nil {
		return Campaign{}, err
	}
	return e.replan(ctx, c, refresh)
}

func (e Engine) replan(ctx context.Context, c Campaign, refresh bool) (Campaign, error) {
	g, snapshotID, err := e.World.Graph(ctx, c.Roots, refresh)
	if err != nil {
		return Campaign{}, err
	}
	plan, err := planner.Make(g, planner.Upgrade{Package: c.Package, Target: c.Target}, e.Registry, e.Accepts)
	switch {
	case errors.Is(err, planner.ErrAlreadyAtTarget):
		c.Status, c.Plan, c.PlanHash, c.SnapshotID = StatusSucceeded, nil, "", snapshotID
	case err != nil:
		return Campaign{}, err
	default:
		hash := sha256.Sum256([]byte(plan.Render()))
		c.Plan, c.PlanHash, c.SnapshotID = &plan, hex.EncodeToString(hash[:]), snapshotID
		c.Status = StatusRunning
		if !plan.Feasible {
			c.Status = StatusBlocked
		}
	}
	c.UpdatedAt = e.Now()
	return c, e.Store.save(ctx, c)
}

// NextState says what the driver should do.
type NextState string

const (
	NextDispatch  NextState = "dispatch"  // execute WorkOrder, then record
	NextSucceeded NextState = "succeeded" // goal reached
	NextExhausted NextState = "exhausted" // budget spent
	NextBlocked   NextState = "blocked"   // needs a changed graph, a revised plan, or a human
)

// Next is the answer to `campaign next`.
type Next struct {
	State    NextState
	Campaign Campaign
	Step     int // live step number; meaningful for NextDispatch
	Steps    int // Completed + remaining
	Reason   string
	// WorkOrder is the deterministic prompt for the live step.
	WorkOrder string
}

// Next reports the live step's work order, or why there is none. It is
// idempotent while a step is in flight: nothing changes until a verdict
// lands, so racing executors all receive the same order.
func (e Engine) Next(ctx context.Context, id string) (Next, error) {
	c, err := e.Store.Get(ctx, id)
	if err != nil {
		return Next{}, err
	}
	if c.Status == StatusSucceeded {
		return Next{State: NextSucceeded, Campaign: c}, nil
	}
	if c.Plan == nil {
		return Next{}, fmt.Errorf("campaign %s has no plan — run `depmaint campaign plan %s`", id, id)
	}
	if c.Status != StatusExhausted && (c.Spent >= c.Budget.MaxAttempts || e.Now().Sub(c.CreatedAt) >= c.Budget.MaxDuration) {
		c.Status, c.UpdatedAt = StatusExhausted, e.Now()
		if err := e.Store.save(ctx, c); err != nil {
			return Next{}, err
		}
	}
	if c.Status == StatusExhausted {
		return Next{State: NextExhausted, Campaign: c, Reason: fmt.Sprintf("%d of %d attempts spent", c.Spent, c.Budget.MaxAttempts)}, nil
	}
	if !c.Plan.Feasible {
		return Next{State: NextBlocked, Campaign: c, Reason: "no feasible coordinated upgrade set; see blockers in the plan"}, nil
	}
	step := c.Completed + 1
	attempts, err := e.Store.Attempts(ctx, id)
	if err != nil {
		return Next{}, err
	}
	for _, a := range attempts {
		if a.Verdict == VerdictRejected && a.Step == step && a.PlanHash == c.PlanHash && a.SnapshotBefore == c.SnapshotID {
			return Next{State: NextBlocked, Campaign: c, Step: step, Reason: fmt.Sprintf(
				"step %d was rejected (%s) on this snapshot and plan; it is not re-issued until the graph changes or the plan is revised", step, a.Reason)}, nil
		}
	}
	g, snapshotID, err := e.World.Graph(ctx, c.Roots, false)
	if err != nil {
		return Next{}, err
	}
	if snapshotID != c.SnapshotID {
		return Next{}, fmt.Errorf("graph moved since campaign %s was planned (%s, now %s) — run `depmaint campaign plan %s`",
			id, c.SnapshotID, snapshotID, id)
	}
	return Next{State: NextDispatch, Campaign: c, Step: step, Steps: c.Completed + len(c.Plan.Steps),
		WorkOrder: renderWorkOrder(c, g, attempts)}, nil
}

// RecordInput is an executor's report.
type RecordInput struct {
	CampaignID string
	Step       int
	Verdict    Verdict
	Reason     string
	Executor   string
	PRURL      string
	CommitSHA  string
}

// Recorded is the outcome of `campaign record`.
type Recorded struct {
	Attempt  Attempt // Attempt.Verdict may be superseded, whatever was reported
	Campaign Campaign
}

// Record appends an attempt. A report for a step already completed — the
// racing executor lost — is stored as superseded: history, but it neither
// re-plans nor gates. A live succeeded verdict re-snapshots and re-plans.
func (e Engine) Record(ctx context.Context, in RecordInput) (Recorded, error) {
	switch in.Verdict {
	case VerdictSucceeded, VerdictFailed:
	case VerdictRejected:
		if in.Reason == "" {
			return Recorded{}, errors.New("a rejected verdict needs a reason")
		}
	default:
		return Recorded{}, fmt.Errorf("verdict %q not recordable (want succeeded, rejected or failed)", in.Verdict)
	}
	c, err := e.Store.Get(ctx, in.CampaignID)
	if err != nil {
		return Recorded{}, err
	}
	if c.Plan == nil {
		return Recorded{}, fmt.Errorf("campaign %s has no plan to record against", c.ID)
	}
	live := c.Completed + 1
	if in.Step > live || in.Step < 1 {
		return Recorded{}, fmt.Errorf("campaign %s has no step %d (live step is %d)", c.ID, in.Step, live)
	}
	executor := in.Executor
	if executor == "" {
		executor = "unlabeled"
	}
	a := Attempt{ID: fmt.Sprintf("%s-a%03d", c.ID, c.Spent+1), CampaignID: c.ID, Step: in.Step, PlanHash: c.PlanHash,
		SnapshotBefore: c.SnapshotID, Verdict: in.Verdict, Reason: in.Reason, Executor: executor,
		PRURL: in.PRURL, CommitSHA: in.CommitSHA, CreatedAt: e.Now()}
	superseded := in.Step < live || c.Status == StatusSucceeded
	if superseded {
		a.Verdict = VerdictSuperseded
	}
	c.Spent++
	if !superseded && in.Verdict == VerdictSucceeded {
		c.Completed++
		// The rest of the plan was a hypothesis; replan on what actually landed.
		replanned, err := e.replan(ctx, c, true)
		if err != nil {
			return Recorded{}, err
		}
		c = replanned
		a.SnapshotAfter = c.SnapshotID
	} else {
		c.UpdatedAt = e.Now()
		if err := e.Store.save(ctx, c); err != nil {
			return Recorded{}, err
		}
	}
	if err := e.Store.appendAttempt(ctx, a); err != nil {
		return Recorded{}, err
	}
	return Recorded{Attempt: a, Campaign: c}, nil
}
