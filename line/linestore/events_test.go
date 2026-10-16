package linestore

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestEventsFilterByCardStationAndSinceOldestFirst(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	all := []Event{
		{At: t0, Kind: EventClaim, Card: "a-1", Station: "maker-1"},
		{At: t0.Add(time.Second), Kind: EventClaim, Card: "a-2", Station: "maker-2", Detail: "first"},
		{At: t0.Add(1500 * time.Millisecond), Kind: EventReap, Card: "a-1", Station: "heijunka-1", Detail: "silent"},
		{At: t0.Add(2 * time.Second), Kind: EventAndonPull, Station: "andon-1"},
	}
	for _, e := range all {
		if err := s.RecordEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name string
		f    EventFilter
		want []Event
	}{
		{"no filter", EventFilter{}, all},
		{"card", EventFilter{Card: "a-1"}, []Event{all[0], all[2]}},
		{"station", EventFilter{Station: "andon-1"}, all[3:]},
		{"since is inclusive and compares sub-second times", EventFilter{Since: t0.Add(1500 * time.Millisecond)}, all[2:]},
		{"card and since", EventFilter{Card: "a-1", Since: t0.Add(time.Second)}, all[2:3]},
		{"no match", EventFilter{Card: "zzz"}, []Event{}},
	}
	for _, tt := range tests {
		got, err := s.Events(ctx, tt.f)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: got %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestEventsReturnsNewestMaxEvents(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for i := range MaxEvents + 2 {
		if err := s.RecordEvent(ctx, Event{At: t0.Add(time.Duration(i) * time.Second), Kind: EventOffer}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Events(ctx, EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxEvents || !got[0].At.Equal(t0.Add(2*time.Second)) || !got[MaxEvents-1].At.Equal(t0.Add(time.Duration(MaxEvents+1)*time.Second)) {
		t.Fatalf("got %d events from %v to %v", len(got), got[0].At, got[len(got)-1].At)
	}
}
