package station

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	// LabelDeferred holds an open card back from makers ("not now"). Unlike
	// closing, it is reversible: the king's `approve` removes it, and a
	// LabelDeferUntilPrefix label lets it lapse by itself.
	LabelDeferred = "line-deferred"
	// LabelDeferUntilPrefix starts the label that carries a deferral's end
	// date (YYYY-MM-DD, UTC). It only has effect while LabelDeferred is set.
	LabelDeferUntilPrefix = "line-defer-until:"
	deferDateLayout       = "2006-01-02"
)

// deferRe is the king's word for "not now": `defer <id>` or
// `defer <id> until YYYY-MM-DD`.
var deferRe = regexp.MustCompile(`^defer ([a-z0-9-]+(\.[0-9]+)*)(?: until ([0-9]{4}-[0-9]{2}-[0-9]{2}))?$`)

// IsDeferred reports whether a card with labels is deferred at now. A
// deferral with an unreadable date stays in force: hiding a card is the safe
// failure.
func IsDeferred(labels []string, now time.Time) bool {
	if !slices.Contains(labels, LabelDeferred) {
		return false
	}
	for _, l := range labels {
		date, ok := strings.CutPrefix(l, LabelDeferUntilPrefix)
		if !ok {
			continue
		}
		until, err := time.Parse(deferDateLayout, date)
		return err != nil || now.Before(until)
	}
	return true
}

// dropDeferred removes the cards deferred at now from a card list.
func dropDeferred(list json.RawMessage, now time.Time) (json.RawMessage, error) {
	var rows []struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(list, &rows); err != nil {
		return nil, fmt.Errorf("parse ready cards: %w", err)
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(list, &raws); err != nil {
		return nil, fmt.Errorf("parse ready cards: %w", err)
	}
	kept := []json.RawMessage{}
	for i, r := range rows {
		if !IsDeferred(r.Labels, now) {
			kept = append(kept, raws[i])
		}
	}
	return json.Marshal(kept)
}

// deferCard defers a card on the king's word and returns the reply to show.
// A new deferral replaces any earlier end date.
func (c *Channel) deferCard(ctx context.Context, id, until, via string) string {
	if c.Cards == nil {
		return fmt.Sprintf("cannot defer %s: cards are not configured", id)
	}
	fail := func(err error) string {
		log.Printf("channel: defer %s: %v", id, err)
		return fmt.Sprintf("could not defer %s", id)
	}
	if until != "" {
		if _, err := time.Parse(deferDateLayout, until); err != nil {
			return fmt.Sprintf("invalid date %s: use YYYY-MM-DD", until)
		}
	}
	labels, err := showCardLabels(ctx, c.Cards, id)
	if err != nil {
		return fail(err)
	}
	for _, l := range labels {
		if strings.HasPrefix(l, LabelDeferUntilPrefix) {
			if err := c.Cards.RemoveLabel(ctx, id, l); err != nil {
				return fail(err)
			}
		}
	}
	if until != "" {
		if err := c.Cards.AddLabel(ctx, id, LabelDeferUntilPrefix+until); err != nil {
			return fail(err)
		}
	}
	if err := c.Cards.AddLabel(ctx, id, LabelDeferred); err != nil {
		return fail(err)
	}
	reply := "deferred " + id
	if until != "" {
		reply += " until " + until
	}
	if err := c.Cards.Comment(ctx, id, reply+" by the king ("+via+")"); err != nil {
		log.Printf("channel: defer %s comment: %v", id, err)
	}
	return reply
}
