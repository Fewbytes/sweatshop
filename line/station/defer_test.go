package station

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var deferTestNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func TestDeferralLapsOnItsDateAndOtherwiseHoldsUntilApproved(t *testing.T) {
	cases := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"not deferred", []string{"line"}, false},
		{"deferred without a date", []string{"line", LabelDeferred}, true},
		{"date in the future", []string{LabelDeferred, LabelDeferUntilPrefix + "2026-10-21"}, true},
		{"day before the date ends", []string{LabelDeferred, LabelDeferUntilPrefix + "2026-10-21"}, true},
		{"date has arrived", []string{LabelDeferred, LabelDeferUntilPrefix + "2026-10-20"}, false},
		{"date passed", []string{LabelDeferred, LabelDeferUntilPrefix + "2026-10-01"}, false},
		{"unreadable date stays deferred", []string{LabelDeferred, LabelDeferUntilPrefix + "soon"}, true},
		{"stale date label without deferral", []string{LabelDeferUntilPrefix + "2026-10-30"}, false},
	}
	for _, c := range cases {
		if got := IsDeferred(c.labels, deferTestNow); got != c.want {
			t.Errorf("%s: IsDeferred(%v) = %v, want %v", c.name, c.labels, got, c.want)
		}
	}
}

func TestReadyListDropsDeferredCardsAndKeepsTheRest(t *testing.T) {
	list := json.RawMessage(`[{"id":"x-1","labels":["line"]},{"id":"x-2","labels":["line","line-deferred"]},` +
		`{"id":"x-3","labels":["line","line-deferred","line-defer-until:2026-10-01"]}]`)
	got, err := dropDeferred(list, deferTestNow)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(got, &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "x-1,x-3" {
		t.Fatalf("ready after dropping deferred = %v, want x-1,x-3 (x-3's deferral lapsed)", ids)
	}
}

func TestKingDefersCardUntilDateReplacingEarlierDate(t *testing.T) {
	cards := &fakeCards{show: `{"id":"x-4","labels":["line","line-defer-until:2026-10-18"]}`}
	ch, fd := newChan(t, cards)
	ctx := context.Background()
	fd.add("1", "5", "x")
	_ = ch.Poll(ctx)
	fd.add("2", "5", "defer x-4")                     // stranger
	fd.add("3", kingID, "defer x-4 until 2026-11-01") // king
	fd.add("4", kingID, "defer x-4 until 2026-13-45") // impossible date
	fd.add("5", kingID, "defer x-4 until tomorrow")   // not the grammar
	fd.add("6", kingID, "please defer x-4")           // not the grammar
	if err := ch.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	want := "show x-4|unlabel x-4 line-defer-until:2026-10-18|label x-4 line-defer-until:2026-11-01|label x-4 line-deferred|" +
		"comment x-4 deferred x-4 until 2026-11-01 by the king (discord message 3)"
	if got := strings.Join(cards.calls, "|"); got != want {
		t.Fatalf("calls:\n got %s\nwant %s", got, want)
	}
}

func TestKingDefersCardIndefinitely(t *testing.T) {
	cards := &fakeCards{show: `[{"id":"x-4","labels":["line"]}]`}
	ch, fd := newChan(t, cards)
	ctx := context.Background()
	fd.add("1", "5", "x")
	_ = ch.Poll(ctx)
	fd.add("2", kingID, "defer x-4")
	if err := ch.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	want := "show x-4|label x-4 line-deferred|comment x-4 deferred x-4 by the king (discord message 2)"
	if got := strings.Join(cards.calls, "|"); got != want {
		t.Fatalf("calls:\n got %s\nwant %s", got, want)
	}
}

func TestStatusReportsDeferredCardsAsHeld(t *testing.T) {
	r := newRig(t)
	r.srv.Cards = &heldCardsFake{list: `[{"id":"x-1","labels":["line","line-deferred"]},` +
		`{"id":"x-2","labels":["line","line-deferred","line-defer-until:2000-01-01"]},{"id":"x-3","labels":["line"]}]`}
	st, err := r.andon.Status(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Held) != 1 || st.Held[0].ID != "x-1" || !strings.Contains(st.Held[0].Reason, "deferred") {
		t.Fatalf("held = %+v, want only x-1 (x-2's deferral lapsed)", st.Held)
	}
}

func TestApproveAutocompleteMarksDeferredCards(t *testing.T) {
	ch := &Channel{Cards: proposedCards{fakeCards: &fakeCards{}, proposed: `[` +
		`{"id":"x-1","title":"Plain","labels":["line-proposed"]},` +
		`{"id":"x-2","title":"Later","labels":["line-proposed","line-deferred"]}]`}}
	choices, err := ch.proposedChoices(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	want := []CommandChoice{{Name: "x-1 Plain", Value: "x-1"}, {Name: "(deferred) x-2 Later", Value: "x-2"}}
	if len(choices) != 2 || choices[0] != want[0] || choices[1] != want[1] {
		t.Fatalf("choices = %v, want %v", choices, want)
	}
}
