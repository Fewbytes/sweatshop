package heijunka

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/station"
)

// NeedKind is what heijunka does with a stale needs:<role> card.
type NeedKind string

const (
	// NeedReroute hands the card to the viceroy, who can answer or re-aim it.
	NeedReroute NeedKind = "reroute"
	// NeedEscalate raises a viceroy-held card to the king.
	NeedEscalate NeedKind = "escalate"
	// NeedPark takes a card the king has not answered off the line, so makers
	// stop reopening it, until an operator removes station.LabelGaveUp.
	NeedPark NeedKind = "park"
)

// NeedAction is the decision for one stale card. To is the role to label the
// card needs:<To> for; it is empty for NeedPark.
type NeedAction struct {
	Kind NeedKind
	To   string
}

// StaleNeed is a card whose needs:<Role> label has gone untouched.
type StaleNeed struct {
	ID   string
	Role string
}

// DecideStaleNeed picks the action for a card stale on needs:<role>. It is
// pure. Splitting a card is not offered: it needs judgment about the card's
// content, which the viceroy applies after a reroute.
func DecideStaleNeed(role string) NeedAction {
	switch role {
	case "king":
		return NeedAction{Kind: NeedPark}
	case "viceroy":
		return NeedAction{Kind: NeedEscalate, To: "king"}
	default:
		return NeedAction{Kind: NeedReroute, To: "viceroy"}
	}
}

// NeedsCards reads and rewrites the needs:<role> labels of cards.
type NeedsCards interface {
	// StaleNeeds lists cards labelled needs:<role> not updated since cutoff.
	StaleNeeds(ctx context.Context, cutoff time.Time) ([]StaleNeed, error)
	// MoveNeed swaps needs:<from> for needs:<to> on the card and comments why.
	MoveNeed(ctx context.Context, id, from, to, note string) error
	// Park removes needs:<from>, labels the card station.LabelGaveUp and
	// comments why.
	Park(ctx context.Context, id, from, note string) error
}

// NeedsTriager applies the constitution's cards.needs_timeout to cards that
// wait on a role nobody answers.
type NeedsTriager struct {
	Cards   NeedsCards
	Timeout time.Duration
	Log     *log.Logger
}

// Triage decides and applies an action for every card stale at now. A card
// that fails is logged and left for the next wake; the first error is
// returned after the rest were tried.
func (n NeedsTriager) Triage(ctx context.Context, now time.Time) error {
	stale, err := n.Cards.StaleNeeds(ctx, now.Add(-n.Timeout))
	if err != nil {
		return fmt.Errorf("list stale needs: %w", err)
	}
	var first error
	for _, s := range stale {
		act := DecideStaleNeed(s.Role)
		note := fmt.Sprintf("heijunka: needs:%s unanswered for over %s; %s", s.Role, n.Timeout, act.Kind)
		var err error
		if act.Kind == NeedPark {
			err = n.Cards.Park(ctx, s.ID, s.Role, note)
		} else {
			err = n.Cards.MoveNeed(ctx, s.ID, s.Role, act.To, note)
		}
		if err != nil {
			err = fmt.Errorf("%s %s: %w", act.Kind, s.ID, err)
			if first == nil {
				first = err
			}
			continue
		}
		if n.Log != nil {
			n.Log.Printf("%s: needs:%s -> %+v", s.ID, s.Role, act)
		}
	}
	return first
}

// NeedsTimeoutFromConstitution reads cards.needs_timeout from the line's
// constitution TOML.
func NeedsTimeoutFromConstitution(data []byte) (time.Duration, error) {
	c, err := constitution.Parse(data)
	if err != nil {
		return 0, err
	}
	return c.Cards.NeedsTimeoutOrDefault(), nil
}

// BeadsNeeds implements NeedsCards with bd run in Dir.
type BeadsNeeds struct{ Dir string }

func (b BeadsNeeds) bd(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "bd", args...)
	cmd.Dir = b.Dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bd %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// StaleNeeds implements NeedsCards. It asks for each possible need target in
// turn, so labels are literal bd arguments rather than a pattern.
func (b BeadsNeeds) StaleNeeds(ctx context.Context, cutoff time.Time) ([]StaleNeed, error) {
	var stale []StaleNeed
	for _, role := range slices.Concat([]string{"king"}, constitution.Roles) {
		out, err := b.bd(ctx, "list", "--label", station.LabelNeedsPrefix+role, "--limit", "0", "--json")
		if err != nil {
			return nil, err
		}
		var rows []struct {
			ID        string    `json:"id"`
			UpdatedAt time.Time `json:"updated_at"`
		}
		if err := json.Unmarshal(out, &rows); err != nil {
			return nil, fmt.Errorf("bd list needs:%s: parse output: %w", role, err)
		}
		for _, r := range rows {
			if r.UpdatedAt.Before(cutoff) {
				stale = append(stale, StaleNeed{ID: r.ID, Role: role})
			}
		}
	}
	return stale, nil
}

// MoveNeed implements NeedsCards.
func (b BeadsNeeds) MoveNeed(ctx context.Context, id, from, to, note string) error {
	if _, err := b.bd(ctx, "label", "add", id, station.LabelNeedsPrefix+to); err != nil {
		return err
	}
	if _, err := b.bd(ctx, "label", "remove", id, station.LabelNeedsPrefix+from); err != nil {
		return err
	}
	_, err := b.bd(ctx, "comments", "add", "--", id, note)
	return err
}

// Park implements NeedsCards.
func (b BeadsNeeds) Park(ctx context.Context, id, from, note string) error {
	if _, err := b.bd(ctx, "label", "add", id, station.LabelGaveUp); err != nil {
		return err
	}
	if _, err := b.bd(ctx, "label", "remove", id, station.LabelNeedsPrefix+from); err != nil {
		return err
	}
	_, err := b.bd(ctx, "comments", "add", "--", id, note)
	return err
}
