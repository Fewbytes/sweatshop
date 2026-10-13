package heijunka

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestDecideStaleNeed(t *testing.T) {
	tests := []struct {
		role string
		want NeedAction
	}{
		{"king", NeedAction{Kind: NeedPark}},
		{"viceroy", NeedAction{Kind: NeedEscalate, To: "king"}},
		{"gemba", NeedAction{Kind: NeedReroute, To: "viceroy"}},
		{"maker", NeedAction{Kind: NeedReroute, To: "viceroy"}},
	}
	for _, tt := range tests {
		if got := DecideStaleNeed(tt.role); got != tt.want {
			t.Errorf("needs:%s: got %+v, want %+v", tt.role, got, tt.want)
		}
	}
}

type fakeNeedsCards struct {
	stale  []StaleNeed
	cutoff time.Time
	calls  []string
	failID string
}

func (f *fakeNeedsCards) StaleNeeds(_ context.Context, cutoff time.Time) ([]StaleNeed, error) {
	f.cutoff = cutoff
	return f.stale, nil
}

func (f *fakeNeedsCards) MoveNeed(_ context.Context, id, from, to, _ string) error {
	if id == f.failID {
		return errors.New("bd down")
	}
	f.calls = append(f.calls, "move "+id+" "+from+"->"+to)
	return nil
}

func (f *fakeNeedsCards) Park(_ context.Context, id, from, _ string) error {
	f.calls = append(f.calls, "park "+id+" "+from)
	return nil
}

func TestTriageAppliesEachDecisionOnceStaleAtTimeout(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	cards := &fakeNeedsCards{stale: []StaleNeed{{"a-1", "gemba"}, {"a-2", "viceroy"}, {"a-3", "king"}}}
	n := NeedsTriager{Cards: cards, Timeout: 24 * time.Hour}
	if err := n.Triage(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if want := now.Add(-24 * time.Hour); !cards.cutoff.Equal(want) {
		t.Errorf("cutoff %v, want %v", cards.cutoff, want)
	}
	want := []string{"move a-1 gemba->viceroy", "move a-2 viceroy->king", "park a-3 king"}
	if !slices.Equal(cards.calls, want) {
		t.Fatalf("calls %q, want %q", cards.calls, want)
	}
}

func TestTriageTriesRemainingCardsAfterOneFails(t *testing.T) {
	cards := &fakeNeedsCards{stale: []StaleNeed{{"a-1", "gemba"}, {"a-2", "king"}}, failID: "a-1"}
	err := NeedsTriager{Cards: cards, Timeout: time.Hour}.Triage(context.Background(), time.Now())
	if err == nil {
		t.Fatal("failed card not reported")
	}
	if !slices.Equal(cards.calls, []string{"park a-2 king"}) {
		t.Fatalf("calls %q: later card skipped", cards.calls)
	}
}
