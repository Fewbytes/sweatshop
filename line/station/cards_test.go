package station

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/constitution"
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

func cardsRig(t *testing.T, cards Cards) (*Server, *httptest.Server, Client, Client) {
	t.Helper()
	srv := &Server{
		Stations: []*Station{
			{Name: "v", Role: "viceroy", Token: "vt"},
			{Name: "m", Role: "maker", Token: "mt"},
		},
		Cards: cards,
	}
	srv.Guard.Constitution.Viceroy.Delegations = []constitution.Delegation{{Offer: "d", Types: []string{"chore"}, Parent: "x-9"}}
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
	_, e2 := m.CardComment(ctx, "x-1", "hi")
	_, e3 := m.CardOffer(ctx, "x-1")
	for _, err := range []error{e1, e2, e3} {
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
