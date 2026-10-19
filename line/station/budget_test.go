package station

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/gitx/gittest"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/linestore"
)

// readyCards lists two ready cards, whatever the filter.
type readyCards struct{ fakeCards }

func (readyCards) List(context.Context, ListFilter) (json.RawMessage, error) {
	return json.RawMessage(`[{"id":"x-1"},{"id":"x-2"}]`), nil
}

type budgetRig struct {
	srv      *Server
	maker    Client
	viceroy  Client
	discord  *fakeDiscordServer
	readyIDs func() []string
}

func newBudgetRig(t *testing.T, budgets ...constitution.Budget) *budgetRig {
	t.Helper()
	ch, fd := newChan(t, nil)
	repo := gitx.Repo{Dir: t.TempDir()}
	ok(t)(gittest.Git(context.Background(), repo.Dir, "init", "--quiet", "--initial-branch=master"))
	ok(t)(gittest.Git(context.Background(), repo.Dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "--allow-empty", "-m", "seed"))
	srv := &Server{
		Guard: guard.Guard{Repo: repo, Ref: "refs/heads/master"},
		Stations: []*Station{
			{Name: "m", Role: "maker", AgentID: "maker-1", Token: "mt"},
			{Name: "v", Role: "viceroy", Token: "vt"},
		},
		Cards:   &readyCards{},
		Store:   openTestStore(t),
		Channel: ch,
	}
	srv.Guard.Constitution.Budgets = budgets
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	r := &budgetRig{srv: srv, maker: Client{URL: ts.URL, Token: "mt"}, viceroy: Client{URL: ts.URL, Token: "vt"}, discord: fd}
	r.readyIDs = func() []string {
		out, err := r.maker.CardsList(context.Background(), ListFilter{Ready: true})
		if err != nil {
			t.Fatal(err)
		}
		var rows []struct{ ID string }
		if err := json.Unmarshal(out, &rows); err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		return ids
	}
	return r
}

func (r *budgetRig) report(t *testing.T, card string, tokens int64) {
	t.Helper()
	if err := r.maker.ReportTokenUsage(context.Background(), card, tokens); err != nil {
		t.Fatalf("report %d tokens on %s: %v", tokens, card, err)
	}
}

func (r *budgetRig) budgets(t *testing.T) []BudgetStatus {
	t.Helper()
	st, err := r.maker.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st.Budgets
}

func TestLineBudgetWarnsThenExhaustsAndWithholdsCardsWithoutStoppingWork(t *testing.T) {
	r := newBudgetRig(t, constitution.Budget{Scope: "line", Tokens: 1000, Period: "24h"})
	ctx := context.Background()

	r.report(t, "x-1", 700)
	if len(r.discord.posted) != 0 {
		t.Fatalf("notice below the warning mark: %v", r.discord.posted)
	}
	r.report(t, "x-1", 150) // 85%
	if len(r.discord.posted) != 1 || !strings.Contains(r.discord.posted[0], "token budget line is at 85%") {
		t.Fatalf("posted %v, want one 85%% warning", r.discord.posted)
	}
	r.report(t, "x-2", 10) // still warning territory: no repeat
	if len(r.discord.posted) != 1 {
		t.Fatalf("warning repeated: %v", r.discord.posted)
	}
	if got := r.readyIDs(); !slices.Equal(got, []string{"x-1", "x-2"}) {
		t.Fatalf("ready before exhaustion = %v", got)
	}

	r.report(t, "x-2", 140) // 1000 of 1000
	if len(r.discord.posted) != 2 || !strings.Contains(r.discord.posted[1], "token budget line exhausted") {
		t.Fatalf("posted %v, want an exhaustion notice second", r.discord.posted)
	}
	if got := r.readyIDs(); len(got) != 0 {
		t.Fatalf("ready after exhaustion = %v, want none offered", got)
	}
	err := r.maker.CardClaim(ctx, "x-1")
	if err == nil || !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("claim on exhausted budget: %v, want 429 exhausted", err)
	}
	// Work already running is never stopped: it can still report its spend.
	r.report(t, "x-1", 50)
	want := BudgetStatus{Scope: "line", Used: 1050, Limit: 1000, Period: "24h", Percent: 105, WarnPercent: 80, Exhausted: true}
	if got := r.budgets(t); len(got) != 1 || got[0] != want {
		t.Fatalf("status budgets = %+v, want [%+v]", got, want)
	}
}

func TestCardBudgetWithholdsOnlyTheOverspentCard(t *testing.T) {
	r := newBudgetRig(t, constitution.Budget{Scope: "card", Tokens: 500, Period: "24h"})
	r.report(t, "x-1", 500)
	r.report(t, "x-2", 100)
	if got := r.readyIDs(); !slices.Equal(got, []string{"x-2"}) {
		t.Fatalf("ready = %v, want only the card under its budget", got)
	}
	if err := r.maker.CardClaim(context.Background(), "x-2"); err != nil {
		t.Fatalf("claim of card under budget: %v", err)
	}
	if err := r.maker.CardClaim(context.Background(), "x-1"); err == nil {
		t.Fatal("claim of overspent card accepted")
	}
}

func TestRoleBudgetBindsThatRoleOnly(t *testing.T) {
	r := newBudgetRig(t, constitution.Budget{Scope: "kaizen", Tokens: 100, Period: "24h"}, constitution.Budget{Scope: "maker", Tokens: 100, Period: "24h"})
	r.report(t, "x-1", 100)
	if got := r.readyIDs(); len(got) != 0 {
		t.Fatalf("maker ready = %v, want none once the maker budget is spent", got)
	}
	kaizen := r.budgets(t)[0]
	if kaizen.Scope != "kaizen" || kaizen.Used != 0 || kaizen.Exhausted {
		t.Fatalf("kaizen budget = %+v, want untouched by maker usage", kaizen)
	}
	// Viceroy reads the same list without the maker's withholding.
	out, err := r.viceroy.CardsList(context.Background(), ListFilter{Ready: true})
	if err != nil || !strings.Contains(string(out), "x-2") {
		t.Fatalf("viceroy list = %s, %v", out, err)
	}
}

func TestUsageReportsRejectBadInput(t *testing.T) {
	r := newBudgetRig(t, constitution.Budget{Scope: "line", Tokens: 10, Period: "1h"})
	ctx := context.Background()
	for name, c := range map[string]struct {
		card   string
		tokens int64
	}{
		"zero tokens":     {"x-1", 0},
		"negative tokens": {"x-1", -5},
		"absurd tokens":   {"x-1", maxReportedTokens + 1},
		"bad card id":     {"X Y", 5},
	} {
		if err := r.maker.ReportTokenUsage(ctx, c.card, c.tokens); err == nil || !strings.Contains(err.Error(), "400") {
			t.Errorf("%s: %v, want 400", name, err)
		}
	}
	if got := r.budgets(t); got[0].Used != 0 {
		t.Fatalf("rejected reports counted: %+v", got[0])
	}
}

func TestBudgetStatusesCountOnlyUsageInsideTheRollingWindow(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	usage := []linestore.TokenUsage{
		{At: now.Add(-25 * time.Hour), Role: "maker", Card: "x-1", Tokens: 900},
		{At: now.Add(-23 * time.Hour), Role: "maker", Card: "x-1", Tokens: 300},
	}
	got := budgetStatuses([]constitution.Budget{{Scope: "line", Tokens: 1000, Period: "24h"}}, usage, now)
	if len(got) != 1 || got[0].Used != 300 || got[0].Exhausted {
		t.Fatalf("statuses = %+v, want 300 used and not exhausted: the 25h-old report aged out", got)
	}
}
