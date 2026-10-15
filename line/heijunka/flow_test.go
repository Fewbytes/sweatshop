package heijunka

import (
	"testing"
	"time"
)

func TestAnalyzeFlowNamesTheStageWhereCardsWaitLongest(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	cards := []FlowCard{
		{ID: "c-queued", Status: "open", Created: ago(10 * time.Minute)},
		{ID: "c-work", Status: "in_progress", Created: ago(30 * time.Hour), Started: ago(20 * time.Hour)},
		{ID: "c-king-old", Status: "open", Labels: []string{"needs:king"}, Created: ago(50 * time.Hour), Updated: ago(30 * time.Hour)},
		{ID: "c-king-new", Status: "open", Labels: []string{"needs:king"}, Created: ago(5 * time.Hour), Updated: ago(2 * time.Hour)},
		{ID: "c-maker", Status: "open", Labels: []string{"needs:maker"}, Created: ago(3 * time.Hour), Updated: ago(time.Hour)},
		{ID: "c-parked", Status: "open", Labels: []string{"line-gave-up", "needs:king"}, Created: ago(900 * time.Hour), Updated: ago(900 * time.Hour)},
		{ID: "c-done-1", Status: "closed", Created: ago(10 * time.Hour), Started: ago(8 * time.Hour), Closed: ago(4 * time.Hour)},
		{ID: "c-done-2", Status: "closed", Created: ago(10 * time.Hour), Started: ago(4 * time.Hour), Closed: ago(2 * time.Hour)},
	}
	r := AnalyzeFlow(cards, now)

	b := r.Bottleneck
	if b == nil || b.Stage != "needs:king" || b.WIP != 2 || b.TotalWait != 32*time.Hour || b.OldestID != "c-king-old" || b.OldestWait != 30*time.Hour {
		t.Fatalf("bottleneck = %+v, want needs:king with 2 cards, 32h total, oldest c-king-old at 30h", b)
	}
	var order []string
	for _, s := range r.Stages {
		order = append(order, s.Stage)
	}
	want := []string{"needs:king", StageWorking, "needs:maker", StageQueued}
	if len(order) != len(want) {
		t.Fatalf("stages %v, want %v (parked card must not count)", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("stages %v, want %v", order, want)
		}
	}
	if r.Closed != 2 || r.MeanLeadTime != 7*time.Hour || r.MeanQueueLatency != 4*time.Hour {
		t.Fatalf("closed=%d lead=%s queue=%s, want 2, 7h, 4h", r.Closed, r.MeanLeadTime, r.MeanQueueLatency)
	}
}

func TestAnalyzeFlowNeverBlamesCardsThatAreBeingWorked(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	r := AnalyzeFlow([]FlowCard{{ID: "c1", Status: "in_progress", Started: now.Add(-100 * time.Hour)}}, now)
	if r.Bottleneck != nil {
		t.Fatalf("bottleneck = %+v, want none", r.Bottleneck)
	}
	if got := r.Wait(StageWorking); got != 100*time.Hour {
		t.Fatalf("working wait = %s, want 100h", got)
	}
}

func TestDecideAddsAMakerWhenCardsWaitLongerThanTheLatencyTarget(t *testing.T) {
	p := DefaultPolicy
	slow := FlowReport{Stages: []StageFlow{{Stage: StageQueued, WIP: 1, OldestWait: p.QueueLatencyTarget + time.Minute}}}
	// One ready card keeps the backlog target at one agent; only latency asks for more.
	s := Snapshot{Ready: 1, Current: Levels{1, 1}, Flow: slow}
	if got := Decide(s, p); got != (Levels{2, 2}) {
		t.Fatalf("slow queue: got %+v, want {2 2}", got)
	}
	s.Flow.Stages[0].OldestWait = p.QueueLatencyTarget - time.Minute
	if got := Decide(s, p); got != (Levels{1, 1}) {
		t.Fatalf("fast queue: got %+v, want {1 1}", got)
	}
}

func TestParseFlowCardsReadsBdListTimestamps(t *testing.T) {
	out := []byte(`[{"id":"c1","status":"closed","labels":["line"],"created_at":"2026-10-14T10:00:00Z","started_at":"2026-10-14T11:00:00Z","updated_at":"2026-10-14T12:00:00Z","closed_at":"2026-10-14T12:00:00Z"}]`)
	got, err := parseFlowCards(out)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	c := got[0]
	if c.ID != "c1" || c.Status != "closed" || c.Started.Sub(c.Created) != time.Hour || c.Closed.Sub(c.Created) != 2*time.Hour {
		t.Fatalf("card = %+v", c)
	}
}
