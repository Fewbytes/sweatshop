package heijunka

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/station"
)

type fakeReapCards struct {
	claimed []ClaimedCard
	moved   map[string]bool // cards whose holder settles before Release
	calls   []string
}

func (f *fakeReapCards) Claimed(context.Context) ([]ClaimedCard, error) { return f.claimed, nil }

func (f *fakeReapCards) Release(_ context.Context, c ClaimedCard, _ string) error {
	if f.moved[c.ID] {
		return ErrCardMoved
	}
	f.calls = append(f.calls, "release "+c.ID)
	return nil
}

func (f *fakeReapCards) Escalate(_ context.Context, c ClaimedCard, _ string) error {
	f.calls = append(f.calls, "escalate "+c.ID)
	return nil
}

func TestAbandonedReason(t *testing.T) {
	seen := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	stations := []station.StationStatus{
		{Name: "maker-1", State: station.StateWorking, Card: "a-1", LastSeen: &seen, Alive: true},
		{Name: "maker-2", State: station.StateWorking, Card: "a-9", LastSeen: &seen, Alive: true},
		{Name: "maker-3", State: station.StateIdle, LastSeen: &seen, Alive: true},
		{Name: "maker-4", State: station.StateWorking, Card: "a-1", LastSeen: &seen},
		{Name: "maker-5", State: station.StateUnseen},
	}
	tests := []struct {
		name     string
		assignee string
		want     string
	}{
		{"alive and working the card", "maker-1", ""},
		{"working a different card", "maker-2", `station maker-2 is working on card "a-9", not this one`},
		{"idle", "maker-3", `station maker-3 is idle on card "", not this one`},
		{"silent past the liveness limit", "maker-4", "station maker-4 silent since 2026-10-15T12:00:00Z (last seen)"},
		{"never seen since line start", "maker-5", ""},
		{"station gone", "maker-9", "station maker-9 no longer exists"},
	}
	for _, tt := range tests {
		if got := AbandonedReason(ClaimedCard{ID: "a-1", Assignee: tt.assignee}, stations); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestReapReleasesOnlyAbandonedClaims(t *testing.T) {
	seen := time.Now()
	st := station.Status{Stations: []station.StationStatus{
		{Name: "maker-1", State: station.StateWorking, Card: "a-1", LastSeen: &seen, Alive: true},
		{Name: "maker-2", State: station.StateWorking, Card: "a-9", LastSeen: &seen, Alive: true},
	}}
	cards := &fakeReapCards{claimed: []ClaimedCard{
		{ID: "a-1", Assignee: "maker-1"},
		{ID: "a-2", Assignee: "maker-2"},
		{ID: "a-3", Assignee: "maker-7"},
	}}
	if err := (Reaper{Cards: cards}).Reap(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if want := []string{"release a-2", "release a-3"}; !slices.Equal(cards.calls, want) {
		t.Fatalf("calls %q, want %q", cards.calls, want)
	}
}

func TestReapEscalatesAfterMaxReapsAndOnlyOnce(t *testing.T) {
	cards := &fakeReapCards{claimed: []ClaimedCard{
		{ID: "a-1", Assignee: "maker-7", Reaps: MaxReaps},
		{ID: "a-2", Assignee: "maker-7", Reaps: MaxReaps + 1, Labels: []string{"needs:viceroy"}},
		{ID: "a-3", Assignee: "maker-7", Reaps: MaxReaps - 1},
	}}
	if err := (Reaper{Cards: cards}).Reap(context.Background(), station.Status{}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"escalate a-1", "release a-3"}; !slices.Equal(cards.calls, want) {
		t.Fatalf("calls %q, want %q", cards.calls, want)
	}
}

func TestReapLeavesCardTheHolderSettledFirst(t *testing.T) {
	cards := &fakeReapCards{
		claimed: []ClaimedCard{{ID: "a-1", Assignee: "maker-7"}, {ID: "a-2", Assignee: "maker-7"}},
		moved:   map[string]bool{"a-1": true},
	}
	if err := (Reaper{Cards: cards}).Reap(context.Background(), station.Status{}); err != nil {
		t.Fatalf("a settled card is not an error: %v", err)
	}
	if want := []string{"release a-2"}; !slices.Equal(cards.calls, want) {
		t.Fatalf("calls %q, want %q", cards.calls, want)
	}
}

type fakeReapRecorder struct{ got []string }

func (f *fakeReapRecorder) RecordReap(_ context.Context, card, detail string) error {
	f.got = append(f.got, card+": "+detail)
	return nil
}

func TestReapRecordsEventForEachReapedCard(t *testing.T) {
	seen := time.Now()
	st := station.Status{Stations: []station.StationStatus{
		{Name: "maker-1", State: station.StateWorking, Card: "a-1", LastSeen: &seen, Alive: true},
	}}
	cards := &fakeReapCards{claimed: []ClaimedCard{{ID: "a-1", Assignee: "maker-1"}, {ID: "a-3", Assignee: "maker-7"}}}
	rec := &fakeReapRecorder{}
	if err := (Reaper{Cards: cards, Events: rec}).Reap(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	want := []string{"a-3: heijunka: reaped (reap 1 of 3): station maker-7 no longer exists"}
	if !slices.Equal(rec.got, want) {
		t.Fatalf("recorded %q, want %q", rec.got, want)
	}
}
