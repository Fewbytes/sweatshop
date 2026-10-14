package station

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
)

const (
	cardCommandName       = "card"
	cardTitleOption       = "title"
	cardDescriptionOption = "description"
	cardTypeOption        = "type"
	cardPriorityOption    = "priority"

	defaultCardType     = "task"
	defaultCardPriority = 2

	// maxCommandOptionChars is Discord's cap on a string option value.
	maxCommandOptionChars = 6000
)

// commandOption is one option of a slash command. Its value is a JSON string
// or number, depending on the option's type.
type commandOption struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
}

func (o commandOption) stringValue() string {
	var s string
	_ = json.Unmarshal(o.Value, &s) // a non-string value reads as empty and fails validation
	return s
}

// fileCard creates the card the king described with /card and puts it on the
// line, returning the reply to show. The caller has checked the king.
func (c *Channel) fileCard(ctx context.Context, options []commandOption, via string) string {
	if c.Cards == nil {
		return "cannot file a card: cards are not configured"
	}
	nc := NewCard{Type: defaultCardType, Priority: defaultCardPriority}
	for _, o := range options {
		switch o.Name {
		case cardTitleOption:
			nc.Title = o.stringValue()
		case cardDescriptionOption:
			nc.Description = o.stringValue()
		case cardTypeOption:
			nc.Type = o.stringValue()
		case cardPriorityOption:
			if err := json.Unmarshal(o.Value, &nc.Priority); err != nil {
				return "invalid priority: want an integer 0..4"
			}
		}
	}
	if err := validateNew(nc); err != nil {
		return "invalid card: " + err.Error()
	}
	raw, err := c.Cards.Create(ctx, nc)
	if err != nil {
		log.Printf("channel: file card: %v", err)
		return "could not file the card"
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || !cardIDRe.MatchString(created.ID) {
		log.Printf("channel: file card: unexpected create output %q: %v", raw, err)
		return "filed a card but could not read its id"
	}
	if reply := c.approveCard(ctx, created.ID, via); reply != "approved "+created.ID {
		return fmt.Sprintf("filed %s but %s", created.ID, reply)
	}
	return "filed " + created.ID
}
