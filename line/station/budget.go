package station

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"slices"
	"time"

	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/linestore"
)

// maxReportedTokens bounds one usage report so a typo cannot exhaust a budget.
const maxReportedTokens = 1_000_000_000

// BudgetStatus is how much of one constitution budget is spent. A card-scope
// budget has one status per card with usage in the window (Card names it).
type BudgetStatus struct {
	Scope       string `json:"scope"`
	Card        string `json:"card,omitempty"`
	Used        int64  `json:"used"`
	Limit       int64  `json:"limit"`
	Period      string `json:"period"`
	Percent     int    `json:"percent"`
	WarnPercent int    `json:"warn_percent"`
	Exhausted   bool   `json:"exhausted"`
}

// budgetStatuses meters usage against budgets over each budget's rolling
// window ending at now. It is pure. A scope with no usage yet is reported
// unspent, except card scope, which has nothing to report until a card spends.
func budgetStatuses(budgets []constitution.Budget, usage []linestore.TokenUsage, now time.Time) []BudgetStatus {
	var out []BudgetStatus
	for _, b := range budgets {
		since := now.Add(-b.PeriodDuration())
		spent := map[string]int64{} // by card for card scope, else under ""
		for _, u := range usage {
			if u.At.Before(since) {
				continue
			}
			switch {
			case b.Scope == constitution.BudgetScopeCard:
				spent[u.Card] += u.Tokens
			case b.Scope == constitution.BudgetScopeLine || b.Scope == u.Role:
				spent[""] += u.Tokens
			}
		}
		if b.Scope != constitution.BudgetScopeCard && len(spent) == 0 {
			spent[""] = 0
		}
		cards := make([]string, 0, len(spent))
		for card := range spent {
			cards = append(cards, card)
		}
		slices.Sort(cards)
		for _, card := range cards {
			used := spent[card]
			out = append(out, BudgetStatus{
				Scope: b.Scope, Card: card, Used: used, Limit: b.Tokens, Period: b.Period,
				Percent: int(used * 100 / b.Tokens), WarnPercent: b.WarnPercentOrDefault(), Exhausted: used >= b.Tokens,
			})
		}
	}
	return out
}

// budgetBlock says why a station of role may not be offered card, or "" when
// no exhausted budget covers it.
func budgetBlock(statuses []BudgetStatus, role, card string) string {
	for _, b := range statuses {
		covers := b.Scope == constitution.BudgetScopeLine || b.Scope == role || (b.Scope == constitution.BudgetScopeCard && b.Card == card)
		if b.Exhausted && covers {
			return fmt.Sprintf("token budget %s is exhausted: %d of %d tokens used in %s", b.Scope, b.Used, b.Limit, b.Period)
		}
	}
	return ""
}

// budgetNotices are the channel messages for budgets that crossed their
// warning or full mark between before and after.
func budgetNotices(before, after []BudgetStatus) []string {
	var notices []string
	for _, a := range after {
		var was BudgetStatus
		for _, b := range before {
			if b.Scope == a.Scope && b.Card == a.Card {
				was = b
			}
		}
		name := "token budget " + a.Scope
		if a.Card != "" {
			name += " for " + a.Card
		}
		spent := fmt.Sprintf("%d of %d tokens used in %s", a.Used, a.Limit, a.Period)
		switch {
		case a.Exhausted && !was.Exhausted:
			notices = append(notices, fmt.Sprintf("%s exhausted: %s. No new cards are offered in this scope until usage ages out of the window; running work continues.", name, spent))
		case !a.Exhausted && a.Percent >= a.WarnPercent && was.Percent < a.WarnPercent:
			notices = append(notices, fmt.Sprintf("%s is at %d%%: %s", name, a.Percent, spent))
		}
	}
	return notices
}

// currentBudgets meters the constitution's budgets against the stored usage.
// Without budgets it reads nothing.
func (s *Server) currentBudgets(ctx context.Context, now time.Time) ([]BudgetStatus, []linestore.TokenUsage, error) {
	budgets := s.Guard.Constitution.Budgets
	if len(budgets) == 0 || s.Store == nil {
		return nil, nil, nil
	}
	window := slices.MaxFunc(budgets, func(a, b constitution.Budget) int {
		return int(a.PeriodDuration() - b.PeriodDuration())
	}).PeriodDuration()
	usage, err := s.Store.TokenUsageSince(ctx, now.Add(-window))
	if err != nil {
		return nil, nil, err
	}
	return budgetStatuses(budgets, usage, now), usage, nil
}

// budgetBlockFor is budgetBlock over the current usage.
func (s *Server) budgetBlockFor(ctx context.Context, role, card string) (string, error) {
	statuses, _, err := s.currentBudgets(ctx, time.Now())
	if err != nil {
		return "", fmt.Errorf("token budgets: %w", err)
	}
	return budgetBlock(statuses, role, card), nil
}

// dropBudgetBlocked removes from a card list the cards no station of role may
// be offered while a budget is exhausted.
func (s *Server) dropBudgetBlocked(ctx context.Context, list json.RawMessage, role string) (json.RawMessage, error) {
	statuses, _, err := s.currentBudgets(ctx, time.Now())
	if err != nil {
		return nil, fmt.Errorf("token budgets: %w", err)
	}
	var rows []struct {
		ID string `json:"id"`
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(list, &rows); err != nil {
		return nil, fmt.Errorf("parse ready cards: %w", err)
	}
	if err := json.Unmarshal(list, &raws); err != nil {
		return nil, fmt.Errorf("parse ready cards: %w", err)
	}
	kept := []json.RawMessage{}
	for i, r := range rows {
		if budgetBlock(statuses, role, r.ID) == "" {
			kept = append(kept, raws[i])
		}
	}
	return json.Marshal(kept)
}

// routeUsage serves POST /v1/usage {card, tokens}: a station reports tokens it
// spent on a card. Usage is self-reported; the line trusts the station's token
// for who reports, and bounds one report by maxReportedTokens. Crossing a
// budget's warning or full mark posts to the channel once.
func (s *Server) routeUsage(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	if err := requireMethod(w, r, http.MethodPost); err != nil {
		return "", err
	}
	if s.Store == nil {
		return "", fail(http.StatusServiceUnavailable, "no line database")
	}
	var body struct {
		Card   string `json:"card"`
		Tokens int64  `json:"tokens"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		return "", err
	}
	if err := validID(body.Card); err != nil {
		return "", err
	}
	if body.Tokens < 1 || body.Tokens > maxReportedTokens {
		return "", fail(http.StatusBadRequest, "tokens must be 1..%d", maxReportedTokens)
	}
	now := time.Now()
	u := linestore.TokenUsage{At: now, Station: st.Name, Role: st.Role, Card: body.Card, Tokens: body.Tokens}
	s.budgetMu.Lock()
	defer s.budgetMu.Unlock()
	before, usage, err := s.currentBudgets(r.Context(), now)
	if err != nil {
		return "", err
	}
	if err := s.Store.RecordTokenUsage(r.Context(), u); err != nil {
		return "", err
	}
	for _, text := range budgetNotices(before, budgetStatuses(s.Guard.Constitution.Budgets, append(usage, u), now)) {
		if err := s.post(r.Context(), text); err != nil {
			log.Printf("station api: post budget notice %q: %v", text, err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return fmt.Sprintf("usage %s %d", body.Card, body.Tokens), nil
}

// ReportTokenUsage tells the line this station spent tokens on card.
func (c Client) ReportTokenUsage(ctx context.Context, card string, tokens int64) error {
	_, err := c.postJSON(ctx, "/v1/usage", map[string]any{"card": card, "tokens": tokens})
	return err
}
