package station

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/linestore"
)

type fakeCards struct {
	calls  []string
	show   string
	err    error
	labels []string
	last   NewCard
}

func (f *fakeCards) rec(s string) error { f.calls = append(f.calls, s); return f.err }
func (f *fakeCards) List(_ context.Context, l ListFilter) (json.RawMessage, error) {
	return json.RawMessage(`[{"id":"x-1"}]`), f.rec("list " + l.Status + "/" + l.Label + "/" + l.Parent)
}
func (f *fakeCards) Show(_ context.Context, id string) (json.RawMessage, error) {
	return json.RawMessage(f.show), f.rec("show " + id)
}
func (f *fakeCards) Create(_ context.Context, c NewCard) (json.RawMessage, error) {
	f.last = c
	return json.RawMessage(`{"id":"x-2"}`), f.rec("create " + c.Title)
}
func (f *fakeCards) Comment(_ context.Context, id, text string) error {
	return f.rec("comment " + id + " " + text)
}
func (f *fakeCards) AddLabel(_ context.Context, id, label string) error {
	f.labels = append(f.labels, label)
	return f.rec("label " + id + " " + label)
}

func (f *fakeCards) RemoveLabel(_ context.Context, id, label string) error {
	return f.rec("unlabel " + id + " " + label)
}

func (f *fakeCards) Claim(_ context.Context, id, agent string) error {
	return f.rec("claim " + id + " " + agent)
}
func (f *fakeCards) Release(_ context.Context, id, agent, note string) error {
	return f.rec("release " + id + " " + agent + " " + note)
}
func (f *fakeCards) Close(_ context.Context, id, agent, reason string) error {
	return f.rec("close " + id + " " + agent + " " + reason)
}

func cardsRig(t *testing.T, cards Cards) (*Server, *httptest.Server, Client, Client) {
	t.Helper()
	srv := &Server{
		Stations: []*Station{
			{Name: "v", Role: "viceroy", Token: "vt"},
			{Name: "m", Role: "maker", Token: "mt"},
		},
		Cards: cards,
	}
	srv.Guard.Constitution.Viceroy.Delegations = []constitution.Delegation{
		{Offer: "line", Types: []string{"chore"}, Parent: "x-9"},
		{Close: true, IDs: []string{"x-1"}, Parent: "x-7"},
		{Close: true, Types: []string{"chore"}, Labels: []string{"obsolete"}},
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, ts, Client{URL: ts.URL, Token: "vt"}, Client{URL: ts.URL, Token: "mt"}
}

func TestCardsReadRoutes(t *testing.T) {
	f := &fakeCards{show: `{"id":"x-1"}`}
	_, _, v, m := cardsRig(t, f)
	for _, c := range []Client{v, m} { // every role may read
		out, err := c.CardsList(context.Background(), ListFilter{Status: "open", Label: "line", Parent: "x-1.2"})
		if err != nil || !strings.Contains(string(out), "x-1") {
			t.Fatalf("list: %s %v", out, err)
		}
		if out, err := c.CardShow(context.Background(), "x-1.2"); err != nil || !strings.Contains(string(out), "x-1") {
			t.Fatalf("show: %s %v", out, err)
		}
	}
	if f.calls[0] != "list open/line/x-1.2" || f.calls[1] != "show x-1.2" {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestCardsInvalidInput(t *testing.T) {
	f := &fakeCards{show: `{}`}
	_, ts, v, _ := cardsRig(t, f)
	ctx := context.Background()
	if _, err := v.CardShow(ctx, "Bad;id"); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("bad id: %v", err)
	}
	if _, err := v.CardsList(ctx, ListFilter{Parent: "../x"}); err == nil {
		t.Fatal("bad parent accepted")
	}
	if _, err := v.CardsList(ctx, ListFilter{Label: "a b"}); err == nil {
		t.Fatal("bad label accepted")
	}
	good := NewCard{Title: "t", Type: "task", Priority: 2}
	bad := []NewCard{
		{Title: "", Type: "task"},
		{Title: strings.Repeat("a", 201), Type: "task"},
		{Title: "t", Type: "task", Description: strings.Repeat("a", 16<<10+1)},
		{Title: "t", Type: "nope"},
		{Title: "t", Type: "task", Priority: 5},
		{Title: "t", Type: "task", Parent: "X Y"},
	}
	for _, nc := range bad {
		if _, err := v.CardCreate(ctx, nc); err == nil || !strings.Contains(err.Error(), "400") {
			t.Fatalf("create %+v: %v", nc, err)
		}
	}
	good.Description = strings.Repeat("a", 16<<10)
	good.Title = strings.Repeat("a", 200)
	if _, err := v.CardCreate(ctx, good); err != nil {
		t.Fatalf("limit-sized create: %v", err)
	}
	if _, err := v.CardComment(ctx, "x-1", " "); err == nil {
		t.Fatal("empty comment accepted")
	}
	// wrong methods and unknown routes
	for path, method := range map[string]string{"/v1/cards/x-1/offer": "GET", "/v1/cards/x-1": "POST", "/v1/cards/x-1/zzz": "GET", "/v1/cards": "DELETE"} {
		req, _ := http.NewRequest(method, ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer vt")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 405 && resp.StatusCode != 404 {
			t.Fatalf("%s %s: %d", method, path, resp.StatusCode)
		}
	}
}

func TestCardsWriteRoutes(t *testing.T) {
	f := &fakeCards{show: `{"id":"x-1"}`}
	_, _, v, _ := cardsRig(t, f)
	ctx := context.Background()
	out, err := v.CardCreate(ctx, NewCard{Title: "-rm", Type: "task", Priority: 1, Parent: "x-1", Description: "d"})
	if err != nil || !strings.Contains(string(out), "x-2") || f.last.Parent != "x-1" || f.last.Description != "d" {
		t.Fatalf("create: %s %v %+v", out, err, f.last)
	}
	if _, err := v.CardComment(ctx, "x-1", "hello\nworld"); err != nil {
		t.Fatal(err)
	}
	if got := f.calls[len(f.calls)-1]; got != "comment x-1 hello\nworld" {
		t.Fatalf("comment call %q", got)
	}
}

func TestCardsWritesNeedViceroy(t *testing.T) {
	f := &fakeCards{show: `{}`}
	_, _, _, m := cardsRig(t, f)
	ctx := context.Background()
	_, e1 := m.CardCreate(ctx, NewCard{Title: "t", Type: "task"})
	_, e3 := m.CardOffer(ctx, "x-1")
	for _, err := range []error{e1, e3} {
		if err == nil || !strings.Contains(err.Error(), "403") {
			t.Fatalf("maker write: %v", err)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("bd called for forbidden writes: %v", f.calls)
	}
}

func TestCardsOffer(t *testing.T) {
	cases := []struct {
		name, id, show, label string
		offered               bool
	}{
		{"type delegated", "x-1", `[{"id":"x-1","issue_type":"chore"}]`, "line", true},
		{"parent in id", "x-9.3", `{"issue_type":"task"}`, "line", true},
		{"parent field", "x-5", `{"issue_type":"task","parent":"x-9"}`, "line", true},
		{"not delegated", "x-1", `{"issue_type":"feature"}`, "line-proposed", false},
	}
	for _, tc := range cases {
		f := &fakeCards{show: tc.show}
		_, _, v, _ := cardsRig(t, f)
		out, err := v.CardOffer(context.Background(), tc.id)
		var got Offer
		if err != nil || json.Unmarshal(out, &got) != nil || got.Offered != tc.offered || got.Label != tc.label {
			t.Fatalf("%s: %s %v", tc.name, out, err)
		}
		if len(f.labels) != 1 || f.labels[0] != tc.label {
			t.Fatalf("%s: labels %v", tc.name, f.labels)
		}
	}
}

func TestCardsViceroyCloseNeedsDelegationAndDecision(t *testing.T) {
	cases := []struct {
		name, id, show string
		want           string // "" means closed
	}{
		{"listed id with decision", "x-1", `[{"id":"x-1","comments":[{"text":"decided"}]}]`, ""},
		{"under delegated parent", "x-7.2", `{"comments":[{"text":"decided"}]}`, ""},
		{"delegated type", "x-3", `{"issue_type":"chore","comments":[{"text":"decided"}]}`, ""},
		{"delegated label", "x-4", `{"issue_type":"task","labels":["obsolete"],"comments":[{"text":"decided"}]}`, ""},
		{"undelegated label", "x-4", `{"issue_type":"task","labels":["other"],"comments":[{"text":"decided"}]}`, "403"},
		{"delegated type without decision", "x-3", `{"issue_type":"chore","comments":[]}`, "409"},
		{"no decision comment", "x-1", `{"comments":[]}`, "409"},
		{"not delegated", "x-2", `{"comments":[{"text":"decided"}]}`, "403"},
		{"offer-only parent grants no close", "x-9.1", `{"comments":[{"text":"decided"}]}`, "403"},
	}
	for _, tc := range cases {
		f := &fakeCards{show: tc.show}
		srv, _, v, _ := cardsRig(t, f)
		srv.Stations[0].AgentID = "vice"
		err := v.CardClose(context.Background(), tc.id, "decided")
		closed := slices.Contains(f.calls, "close "+tc.id+" vice decided")
		if tc.want == "" && (err != nil || !closed) {
			t.Fatalf("%s: err %v calls %v", tc.name, err, f.calls)
		}
		if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want) || closed) {
			t.Fatalf("%s: err %v calls %v", tc.name, err, f.calls)
		}
	}
}

func TestCardsUnconfiguredAndErrors(t *testing.T) {
	_, _, v, m := cardsRig(t, nil)
	for _, err := range []error{
		func() error { _, e := v.CardsList(context.Background(), ListFilter{}); return e }(),
		func() error { _, e := v.CardCreate(context.Background(), NewCard{Title: "t", Type: "task"}); return e }(),
	} {
		if err == nil || !strings.Contains(err.Error(), "503") {
			t.Fatalf("want 503: %v", err)
		}
	}
	if _, err := m.CardShow(context.Background(), "x-1"); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("want 503: %v", err)
	}
	f := &fakeCards{show: `{}`, err: errors.New("boom")}
	_, _, v, _ = cardsRig(t, f)
	if _, err := v.CardShow(context.Background(), "x-1"); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("want 500: %v", err)
	}
}

func TestBeadsCardsRequiresBd(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := (BeadsCards{Dir: t.TempDir()}).Show(context.Background(), "x-1"); err == nil {
		t.Fatal("expected error without bd")
	}
}

func TestBeadsCardsCloseRunsAsStation(t *testing.T) {
	bin := t.TempDir()
	argvFile := filepath.Join(bin, "argv")
	script := "#!/bin/sh\necho \"$@\" > " + argvFile + "\n"
	if err := os.WriteFile(filepath.Join(bin, "bd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if err := (BeadsCards{Dir: t.TempDir()}).Close(context.Background(), "x-1", "maker-1", "done"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	if want := "close x-1 --reason done --actor maker-1\n"; string(got) != want {
		t.Fatalf("bd argv %q, want %q", got, want)
	}
}

func TestCardsGiveUpEscalatesToKingWhenAsked(t *testing.T) {
	f := &fakeCards{show: `[{"id":"x-1","assignee":"m"}]`}
	srv, _, _, m := cardsRig(t, f)
	srv.Stations[1].AgentID = "m"
	if err := m.CardGiveUp(context.Background(), "x-1", true); err != nil {
		t.Fatal(err)
	}
	if want := []string{LabelGaveUp, LabelNeedsPrefix + "king"}; !slices.Equal(f.labels, want) {
		t.Fatalf("labels %q, want %q", f.labels, want)
	}
}

func TestCardsMakerSettlementRoutes(t *testing.T) {
	f := &fakeCards{show: `[{"id":"x-1","assignee":"m"}]`}
	srv, _, v, m := cardsRig(t, f)
	srv.Stations[1].AgentID = "m"
	ctx := context.Background()
	if err := m.CardClaim(ctx, "x-1"); err != nil {
		t.Fatal(err)
	}
	if err := m.CardRelease(ctx, "x-1", "again"); err != nil {
		t.Fatal(err)
	}
	if err := m.CardClose(ctx, "x-1", "done"); err != nil {
		t.Fatal(err)
	}
	if err := m.CardGiveUp(ctx, "x-1", false); err != nil {
		t.Fatal(err)
	}
	if out, err := m.CardsList(ctx, ListFilter{Label: "line", Ready: true}); err != nil || !strings.Contains(string(out), "x-1") {
		t.Fatalf("ready list: %s %v", out, err)
	}
	if err := v.CardClaim(ctx, "x-1"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("viceroy claim: %v", err)
	}
	if _, err := m.CardOffer(ctx, "x-1"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("maker offer: %v", err)
	}
	want := []string{
		"claim x-1 m", "show x-1", "release x-1 m again", "show x-1", "close x-1 m done", "show x-1",
		"label x-1 " + LabelGaveUp, "list /line/",
	}
	if !slices.Equal(f.calls, want) {
		t.Fatalf("calls %q, want %q", f.calls, want)
	}
}

func TestCardsMakerCannotSettleOthersCards(t *testing.T) {
	f := &fakeCards{show: `{"id":"x-1","assignee":"other"}`}
	_, _, _, m := cardsRig(t, f)
	if err := m.CardClose(context.Background(), "x-1", "done"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("close: %v", err)
	}
	if slices.Contains(f.calls, "close x-1 m done") {
		t.Fatalf("calls %q", f.calls)
	}
}

func TestCardsAgentCommentsOnOwnCardOnly(t *testing.T) {
	f := &fakeCards{show: `[{"id":"x-1","assignee":"m"}]`}
	srv, _, _, m := cardsRig(t, f)
	srv.Stations[1].AgentID = "m"
	ctx := context.Background()
	if _, err := m.CardComment(ctx, "x-1", "stuck on X"); err != nil {
		t.Fatalf("own card: %v", err)
	}
	f.show = `[{"id":"x-2","assignee":"other"}]`
	if _, err := m.CardComment(ctx, "x-2", "hi"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("other's card: %v", err)
	}
	if slices.Contains(f.calls, "comment x-2 hi") {
		t.Fatalf("calls %q", f.calls)
	}
}

func TestCardsAgentCommentCapResetsOnClaim(t *testing.T) {
	f := &fakeCards{show: `[{"id":"x-1","assignee":"m"}]`}
	srv, _, _, m := cardsRig(t, f)
	srv.Stations[1].AgentID = "m"
	srv.Guard.Constitution.Cards.CommentCap = 2
	ctx := context.Background()
	for range 2 {
		if _, err := m.CardComment(ctx, "x-1", "note"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.CardComment(ctx, "x-1", "note"); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("third comment: %v", err)
	}
	if err := m.CardClaim(ctx, "x-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CardComment(ctx, "x-1", "note"); err != nil {
		t.Fatalf("after re-claim: %v", err)
	}
}

func TestCardsAgentCommentCapDefaultsToFive(t *testing.T) {
	f := &fakeCards{show: `[{"id":"x-1","assignee":"m"}]`}
	srv, _, _, m := cardsRig(t, f)
	srv.Stations[1].AgentID = "m"
	for i := range constitution.DefaultCommentCap {
		if _, err := m.CardComment(context.Background(), "x-1", "note"); err != nil {
			t.Fatalf("comment %d: %v", i, err)
		}
	}
	if _, err := m.CardComment(context.Background(), "x-1", "note"); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("over default cap: %v", err)
	}
}

func TestCardsAgentRaisesNeedsOnOwnCard(t *testing.T) {
	f := &fakeCards{show: `[{"id":"x-1","assignee":"m"}]`}
	srv, _, v, m := cardsRig(t, f)
	srv.Stations[1].AgentID = "m"
	ctx := context.Background()
	if err := m.CardNeeds(ctx, "x-1", "king"); err != nil {
		t.Fatal(err)
	}
	if want := "label x-1 needs:king"; f.calls[len(f.calls)-1] != want {
		t.Fatalf("calls %q, want last %q", f.calls, want)
	}
	if err := m.CardNeeds(ctx, "x-1", "bogus"); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("unknown role: %v", err)
	}
	if err := v.CardNeeds(ctx, "x-1", "king"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("viceroy needs: %v", err)
	}
	f.show = `[{"id":"x-2","assignee":"other"}]`
	if err := m.CardNeeds(ctx, "x-2", "gemba"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("other's card: %v", err)
	}
	if slices.Contains(f.labels, "needs:gemba") || slices.Contains(f.labels, "needs:bogus") {
		t.Fatalf("labels %q", f.labels)
	}
}

func TestBeadsCardsShowAttachesComments(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
case "$1" in
show) echo '[{"id":"x-1","comment_count":1,"comments_omitted":true}]' ;;
comments) echo '[{"text":"decided"}]' ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "bd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	out, err := (BeadsCards{Dir: t.TempDir()}).Show(context.Background(), "x-1")
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		ID       string            `json:"id"`
		Count    int               `json:"comment_count"`
		Omitted  *bool             `json:"comments_omitted"`
		Comments []json.RawMessage `json:"comments"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "x-1" || got[0].Count != 1 || got[0].Omitted != nil || len(got[0].Comments) != 1 {
		t.Fatalf("show output %s", out)
	}
}

// epicShow answers Show by card ID, like bd show for a small tree.
type epicShowCards struct {
	fakeCards
	shows map[string]string
}

func (f *epicShowCards) Show(_ context.Context, id string) (json.RawMessage, error) {
	return json.RawMessage(f.shows[id]), f.rec("show " + id)
}

func TestCardsOfferInheritsApprovalFromLedgeredOpenEpic(t *testing.T) {
	cases := []struct {
		name, id, show string
		epic           string
		ledgered       bool
		offered        bool
	}{
		{"task under approved epic", "x-3.1", `{"issue_type":"task"}`, `{"issue_type":"epic","status":"open"}`, true, true},
		{"epic itself", "x-3", `{"issue_type":"epic"}`, `{"issue_type":"epic","status":"open"}`, true, false},
		{"epic carries only the label", "x-3.1", `{"issue_type":"task"}`, `{"issue_type":"epic","status":"open","labels":["line"]}`, false, false},
		{"closed epic", "x-3.1", `{"issue_type":"task"}`, `{"issue_type":"epic","status":"closed"}`, true, false},
		{"ledgered card is not an epic", "x-3.1", `{"issue_type":"task"}`, `{"issue_type":"feature","status":"open"}`, true, false},
	}
	for _, tc := range cases {
		f := &epicShowCards{shows: map[string]string{tc.id: tc.show, "x-3": tc.epic}}
		srv, _, v, _ := cardsRig(t, f)
		srv.Guard.Constitution.Viceroy.Delegations = []constitution.Delegation{{Offer: "line", Inherit: true}}
		srv.Epics = &EpicApprovals{Path: filepath.Join(t.TempDir(), "approved-epics.json")}
		if tc.ledgered {
			if err := srv.Epics.Approve("x-3", "test"); err != nil {
				t.Fatal(err)
			}
		}
		out, err := v.CardOffer(context.Background(), tc.id)
		var got Offer
		if err != nil || json.Unmarshal(out, &got) != nil || got.Offered != tc.offered {
			t.Fatalf("%s: %s %v", tc.name, out, err)
		}
	}
}

func TestClosedEpicStaysUnapprovedAfterReopen(t *testing.T) {
	f := &epicShowCards{shows: map[string]string{"x-3.1": `{"issue_type":"task"}`, "x-3": `{"issue_type":"epic","status":"closed"}`}}
	srv, _, v, _ := cardsRig(t, f)
	srv.Guard.Constitution.Viceroy.Delegations = []constitution.Delegation{{Offer: "line", Inherit: true}}
	srv.Epics = &EpicApprovals{Path: filepath.Join(t.TempDir(), "approved-epics.json")}
	if err := srv.Epics.Approve("x-3", "test"); err != nil {
		t.Fatal(err)
	}
	if out, err := v.CardOffer(context.Background(), "x-3.1"); err != nil || !strings.Contains(string(out), `"line-proposed"`) {
		t.Fatalf("closed: %s %v", out, err)
	}
	f.shows["x-3"] = `{"issue_type":"epic","status":"open"}` // reopened
	if out, err := v.CardOffer(context.Background(), "x-3.1"); err != nil || !strings.Contains(string(out), `"line-proposed"`) {
		t.Fatalf("reopened: %s %v", out, err)
	}
}

func TestClosingEpicRevokesApproval(t *testing.T) {
	f := &epicShowCards{shows: map[string]string{"x-3": `{"issue_type":"epic","comments":[{"text":"decided"}]}`}}
	srv, _, v, _ := cardsRig(t, f)
	srv.Guard.Constitution.Viceroy.Delegations = []constitution.Delegation{{Close: true, IDs: []string{"x-3"}}}
	srv.Stations[0].AgentID = "vice"
	srv.Epics = &EpicApprovals{Path: filepath.Join(t.TempDir(), "approved-epics.json")}
	if err := srv.Epics.Approve("x-3", "test"); err != nil {
		t.Fatal(err)
	}
	if err := v.CardClose(context.Background(), "x-3", "done"); err != nil {
		t.Fatal(err)
	}
	if ok, err := srv.Epics.Approved("x-3"); err != nil || ok {
		t.Fatalf("approved=%v err=%v", ok, err)
	}
}

func TestEventsRecordCardActionsAndAreReadableByEveryRole(t *testing.T) {
	f := &fakeCards{show: `{"id":"x-1","assignee":"mt-agent","issue_type":"chore","parent":"x-9"}`}
	srv, _, v, m := cardsRig(t, f)
	srv.Store = openTestStore(t)
	srv.Stations[1].AgentID = "mt-agent"
	ctx := context.Background()
	if _, err := v.CardOffer(ctx, "x-1"); err != nil {
		t.Fatal(err)
	}
	if err := m.CardClaim(ctx, "x-1"); err != nil {
		t.Fatal(err)
	}
	if err := m.CardRelease(ctx, "x-1", "stuck"); err != nil {
		t.Fatal(err)
	}
	if err := m.CardGiveUp(ctx, "x-1", false); err != nil {
		t.Fatal(err)
	}
	if err := m.CardClose(ctx, "x-1", "done"); err != nil {
		t.Fatal(err)
	}
	if err := m.CardClaim(ctx, "x-2"); err != nil {
		t.Fatal(err)
	}
	type row struct {
		Kind          linestore.EventKind
		Card, Station string
	}
	rowsOf := func(events []linestore.Event) []row {
		var rows []row
		for _, e := range events {
			rows = append(rows, row{e.Kind, e.Card, e.Station})
		}
		return rows
	}
	got, err := v.Events(ctx, "", "x-1", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []row{
		{linestore.EventOffer, "x-1", "v"}, {linestore.EventClaim, "x-1", "m"}, {linestore.EventRelease, "x-1", "m"},
		{linestore.EventGiveUp, "x-1", "m"}, {linestore.EventClose, "x-1", "m"},
	}
	if !slices.Equal(rowsOf(got), want) || got[2].Detail != "stuck" {
		t.Fatalf("events of x-1 = %+v, want %v", got, want)
	}
	byStation, err := m.Events(ctx, "1h", "", "m")
	if err != nil || len(byStation) != 5 || byStation[4].Card != "x-2" {
		t.Fatalf("events of station m = %+v, %v", byStation, err)
	}
	if _, err := v.Events(ctx, "bogus", "", ""); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("bad since: %v", err)
	}
	if err := v.RecordReap(ctx, "x-1", "silent"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("only heijunka may record reaps: %v", err)
	}
}

func TestEventsRecordReapFromHeijunkaStation(t *testing.T) {
	srv, _, v, _ := cardsRig(t, &fakeCards{})
	srv.Store = openTestStore(t)
	srv.Stations = append(srv.Stations, &Station{Name: "h", Role: heijunkaRole, Token: "ht"})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	h := Client{URL: ts.URL, Token: "ht"}
	if err := h.RecordReap(context.Background(), "x-1", "station m silent"); err != nil {
		t.Fatal(err)
	}
	got, err := v.Events(context.Background(), "", "", "")
	if err != nil || len(got) != 1 || got[0].Kind != linestore.EventReap || got[0].Card != "x-1" || got[0].Station != "h" || got[0].Detail != "station m silent" {
		t.Fatalf("events = %+v, %v", got, err)
	}
}
