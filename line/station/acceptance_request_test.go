package station

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/linestore"
)

// acceptanceRig is a line with a maker and a gemba station over fake cards.
func acceptanceRig(t *testing.T, show string) (*fakeCards, Client, Client) {
	t.Helper()
	f := &fakeCards{show: show}
	srv, ts, _, maker := cardsRig(t, f)
	srv.Stations = append(srv.Stations, &Station{Name: "g", Role: "gemba", Token: "gt", AgentID: "g-agent"})
	srv.Store = openTestStore(t)
	return f, maker, Client{URL: ts.URL, Token: "gt"}
}

func goodAcceptanceRequest() AcceptanceRequest {
	return AcceptanceRequest{Path: "acceptance/line_init_smoke_test.go", Change: AcceptanceFix,
		EvidenceKind: EvidenceFailingRun, Evidence: "run 7: asserts 200, spec says 204"}
}

func TestMakerRequestsAcceptanceChangeAsGembaCard(t *testing.T) {
	f, maker, gemba := acceptanceRig(t, `{}`)
	ctx := context.Background()
	res, err := maker.RequestAcceptanceChange(ctx, goodAcceptanceRequest())
	if err != nil || res.Card != "x-2" {
		t.Fatalf("request: %+v, %v", res, err)
	}
	if !strings.Contains(f.last.Description, "run 7: asserts 200, spec says 204") ||
		!strings.Contains(f.last.Title, "acceptance/line_init_smoke_test.go") {
		t.Fatalf("card %+v does not carry the request", f.last)
	}
	if want := []string{LabelGemba, LabelAcceptanceRequest}; !slices.Equal(f.labels, want) {
		t.Fatalf("labels %v, want %v", f.labels, want)
	}
	events, _ := gemba.Events(ctx, "", "x-2", "")
	if len(events) != 1 || events[0].Kind != linestore.EventAcceptanceRequest || events[0].Station != "m" {
		t.Fatalf("events %+v", events)
	}
}

func TestAcceptanceRequestRejectsWhatIsNotAnEvidencedChangeToAcceptanceTests(t *testing.T) {
	bad := map[string]func(*AcceptanceRequest){
		"outside acceptance tree": func(a *AcceptanceRequest) { a.Path = "line/station/cards_test.go" },
		"path escapes tree":       func(a *AcceptanceRequest) { a.Path = "acceptance/../justfile" },
		"unknown change":          func(a *AcceptanceRequest) { a.Change = "weaken" },
		"unknown evidence kind":   func(a *AcceptanceRequest) { a.EvidenceKind = "hunch" },
		"no evidence":             func(a *AcceptanceRequest) { a.Evidence = "  " },
	}
	for name, mutate := range bad {
		f, maker, gemba := acceptanceRig(t, `{}`)
		a := goodAcceptanceRequest()
		mutate(&a)
		if _, err := maker.RequestAcceptanceChange(context.Background(), a); err == nil || !strings.Contains(err.Error(), "400") {
			t.Fatalf("%s: err %v", name, err)
		}
		if len(f.calls) != 0 {
			t.Fatalf("%s: created cards: %v", name, f.calls)
		}
		if _, err := gemba.RequestAcceptanceChange(context.Background(), goodAcceptanceRequest()); err == nil || !strings.Contains(err.Error(), "403") {
			t.Fatalf("%s: gemba filed a request: %v", name, err)
		}
	}
}

func TestGembaAnswersAreRecordedWithReasons(t *testing.T) {
	const requestCard = `{"id":"x-2","labels":["gemba","acceptance-request"]}`
	cases := []struct {
		state AcceptanceAnswerState
		calls []string
	}{
		{AcceptanceAccepted, []string{"show x-2", "comment x-2 accepted by g: spec agrees", "label x-2 acceptance-accepted"}},
		{AcceptanceDeclined, []string{"show x-2", "comment x-2 declined by g: spec agrees", "label x-2 acceptance-declined", "close x-2 g-agent declined: spec agrees"}},
	}
	for _, tc := range cases {
		f, maker, gemba := acceptanceRig(t, requestCard)
		ctx := context.Background()
		if err := gemba.AnswerAcceptanceRequest(ctx, "x-2", AcceptanceAnswer{State: tc.state, Reason: "spec agrees"}); err != nil {
			t.Fatalf("%s: %v", tc.state, err)
		}
		if !slices.Equal(f.calls, tc.calls) {
			t.Fatalf("%s: calls %v, want %v", tc.state, f.calls, tc.calls)
		}
		events, _ := maker.Events(ctx, "", "x-2", "")
		if len(events) != 1 || events[0].Kind != linestore.EventAcceptanceAnswer || events[0].Detail != string(tc.state)+": spec agrees" {
			t.Fatalf("%s: events %+v", tc.state, events)
		}
	}
}

func TestAcceptanceAnswerRefusals(t *testing.T) {
	cases := []struct {
		name   string
		show   string
		answer AcceptanceAnswer
		as     func(maker, gemba Client) Client
		want   string
	}{
		{"maker cannot answer", `{"labels":["acceptance-request"]}`, AcceptanceAnswer{State: AcceptanceAccepted, Reason: "r"}, func(m, _ Client) Client { return m }, "403"},
		{"decline needs a reason", `{"labels":["acceptance-request"]}`, AcceptanceAnswer{State: AcceptanceDeclined}, func(_, g Client) Client { return g }, "400"},
		{"unknown state", `{"labels":["acceptance-request"]}`, AcceptanceAnswer{State: "maybe", Reason: "r"}, func(_, g Client) Client { return g }, "400"},
		{"not a request card", `{"labels":["gemba"]}`, AcceptanceAnswer{State: AcceptanceAccepted, Reason: "r"}, func(_, g Client) Client { return g }, "409"},
		{"already answered", `{"labels":["acceptance-request","acceptance-declined"]}`, AcceptanceAnswer{State: AcceptanceAccepted, Reason: "r"}, func(_, g Client) Client { return g }, "409"},
	}
	for _, tc := range cases {
		f, maker, gemba := acceptanceRig(t, tc.show)
		err := tc.as(maker, gemba).AnswerAcceptanceRequest(context.Background(), "x-2", tc.answer)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err %v, want %s", tc.name, err, tc.want)
		}
		for _, c := range f.calls {
			if !strings.HasPrefix(c, "show") {
				t.Fatalf("%s: wrote to the card: %v", tc.name, f.calls)
			}
		}
	}
}
