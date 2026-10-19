package constitution

import (
	"fmt"
	"slices"
	"time"
)

// Budget scopes besides a role name (a Budget with Scope "maker" limits what
// all maker stations together spend).
const (
	BudgetScopeLine = "line" // all stations together
	BudgetScopeCard = "card" // each card separately
)

// DefaultBudgetWarnPercent is the share of a budget at which the line first
// tells the king it is running low.
const DefaultBudgetWarnPercent = 80

// Budget caps the tokens the stations in Scope may spend over a rolling
// Period. The line meters station-reported usage and withholds cards from
// makers once a budget is exhausted; running work is never stopped. The king
// changes budgets like any other constitution rule: by approving a proposal.
type Budget struct {
	// Scope is BudgetScopeLine, BudgetScopeCard or one of Roles.
	Scope string `toml:"scope"`
	// Tokens is the most tokens the scope may spend within Period.
	Tokens int64 `toml:"tokens"`
	// Period (Go duration, such as 24h or 168h) is the rolling window usage
	// counts over.
	Period string `toml:"period"`
	// WarnPercent (1..99) is when the channel is told the budget runs low;
	// 0 means DefaultBudgetWarnPercent. Reaching 100 is always announced.
	WarnPercent int `toml:"warn_percent,omitempty"`
}

// PeriodDuration is the budget's window. Parse rejects malformed periods, so
// a bad one here can only come from code.
func (b Budget) PeriodDuration() time.Duration {
	d, err := time.ParseDuration(b.Period)
	if err != nil {
		panic(fmt.Sprintf("budget period %q: %v", b.Period, err))
	}
	return d
}

// WarnPercentOrDefault is WarnPercent with the default applied.
func (b Budget) WarnPercentOrDefault() int {
	if b.WarnPercent == 0 {
		return DefaultBudgetWarnPercent
	}
	return b.WarnPercent
}

func (c Constitution) validateBudgets() error {
	seen := map[string]bool{}
	for i, b := range c.Budgets {
		if b.Scope != BudgetScopeLine && b.Scope != BudgetScopeCard && !slices.Contains(Roles, b.Scope) {
			return fmt.Errorf("budgets[%d].scope %q: want %s, %s or a role", i, b.Scope, BudgetScopeLine, BudgetScopeCard)
		}
		if seen[b.Scope] {
			return fmt.Errorf("budgets[%d].scope %q: already has a budget", i, b.Scope)
		}
		seen[b.Scope] = true
		if b.Tokens < 1 {
			return fmt.Errorf("budgets[%d].tokens %d: must be positive", i, b.Tokens)
		}
		if d, err := time.ParseDuration(b.Period); err != nil || d <= 0 {
			return fmt.Errorf("budgets[%d].period %q: want a positive duration like 24h", i, b.Period)
		}
		if b.WarnPercent < 0 || b.WarnPercent > 99 {
			return fmt.Errorf("budgets[%d].warn_percent %d: want 1..99", i, b.WarnPercent)
		}
	}
	return nil
}
