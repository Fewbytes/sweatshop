package heijunka

import (
	"slices"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/line/station"
)

// StageQueued and StageWorking are the flow stages every open card is in;
// StageNeeds(role) covers a card waiting on a role to answer.
const (
	StageQueued  = "queued"  // ready, waiting for a maker to claim it
	StageWorking = "working" // claimed by a maker
)

// StageNeeds names the stage of a card waiting on role (label needs:<role>).
func StageNeeds(role string) string { return station.LabelNeedsPrefix + role }

// FlowCard is one card as flow analysis sees it. Times are zero when unknown.
type FlowCard struct {
	ID      string
	Status  string // "open", "in_progress" or "closed"
	Labels  []string
	Created time.Time
	Started time.Time // when a maker claimed it
	Updated time.Time // last change, including label and comment changes
	Closed  time.Time
}

// StageFlow is the load on one stage: how many open cards sit in it and how
// long they have been there.
type StageFlow struct {
	Stage      string
	WIP        int
	TotalWait  time.Duration // summed over its cards; grows with both count and age
	OldestID   string
	OldestWait time.Duration
}

// FlowReport is the line's flow at one instant. Throughput is deliberately
// absent: rate says how much finished, not where work is stuck.
type FlowReport struct {
	// Stages lists the stages that hold cards, the largest TotalWait first.
	Stages []StageFlow
	// Bottleneck is the waiting stage with the largest TotalWait, nil when no
	// card waits. StageWorking is never the bottleneck: a card being worked
	// is flowing, and makers falling behind shows up as StageQueued.
	Bottleneck *StageFlow
	// Closed cards give the lead-time and queue-latency means; both are zero
	// when nothing has closed.
	Closed           int
	MeanLeadTime     time.Duration // created to closed
	MeanQueueLatency time.Duration // created to claimed
}

// Wait returns the oldest wait in stage, zero if no card is in it.
func (r FlowReport) Wait(stage string) time.Duration {
	for _, s := range r.Stages {
		if s.Stage == stage {
			return s.OldestWait
		}
	}
	return 0
}

// cardStage returns where an open card is and since when. Parked and deferred
// cards are off the line and return ok=false.
func cardStage(c FlowCard, now time.Time) (stage string, since time.Time, ok bool) {
	if slices.Contains(c.Labels, station.LabelGaveUp) || station.IsDeferred(c.Labels, now) {
		return "", time.Time{}, false
	}
	for _, l := range c.Labels {
		if role, found := strings.CutPrefix(l, station.LabelNeedsPrefix); found {
			return StageNeeds(role), c.Updated, true
		}
	}
	if c.Status == "in_progress" {
		return StageWorking, c.Started, true
	}
	return StageQueued, c.Created, true
}

// AnalyzeFlow measures where cards wait at now and names the bottleneck. It
// is pure.
func AnalyzeFlow(cards []FlowCard, now time.Time) FlowReport {
	var r FlowReport
	byStage := map[string]*StageFlow{}
	var lead, queue time.Duration
	var queueSamples int
	for _, c := range cards {
		if c.Status == "closed" {
			r.Closed++
			lead += c.Closed.Sub(c.Created)
			if !c.Started.IsZero() {
				queue += c.Started.Sub(c.Created)
				queueSamples++
			}
			continue
		}
		stage, since, ok := cardStage(c, now)
		if !ok {
			continue
		}
		wait := max(now.Sub(since), 0)
		s := byStage[stage]
		if s == nil {
			s = &StageFlow{Stage: stage}
			byStage[stage] = s
		}
		s.WIP++
		s.TotalWait += wait
		if wait > s.OldestWait || s.OldestID == "" {
			s.OldestID, s.OldestWait = c.ID, wait
		}
	}
	for _, s := range byStage {
		r.Stages = append(r.Stages, *s)
	}
	slices.SortFunc(r.Stages, func(a, b StageFlow) int {
		if a.TotalWait != b.TotalWait {
			return int(b.TotalWait - a.TotalWait) // durations here are far below overflow
		}
		return strings.Compare(a.Stage, b.Stage)
	})
	for i := range r.Stages {
		if r.Stages[i].Stage != StageWorking {
			r.Bottleneck = &r.Stages[i]
			break
		}
	}
	if r.Closed > 0 {
		r.MeanLeadTime = lead / time.Duration(r.Closed)
	}
	if queueSamples > 0 {
		r.MeanQueueLatency = queue / time.Duration(queueSamples)
	}
	return r
}
