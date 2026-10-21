package station

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"slices"
)

// answerRe is the king's reply to a question parked on a card:
// `answer <id> <text>`. The text may span lines.
var answerRe = regexp.MustCompile(`^answer ([a-z0-9-]+(\.[0-9]+)*) ((?s:.+))$`)

// answerCard records the king's answer as a comment on a card waiting on
// needs:king and unblocks it, returning the reply to show. The answer is data
// on the card, never a command to an agent (P1). The caller has checked the
// king. The comment lands before the label comes off, so a failure in between
// leaves the card blocked with the answer visible, not unblocked without it.
func (c *Channel) answerCard(ctx context.Context, id, text, via string) string {
	if c.Cards == nil {
		return fmt.Sprintf("cannot answer %s: cards are not configured", id)
	}
	fail := func(err error) string {
		log.Printf("channel: answer %s: %v", id, err)
		return fmt.Sprintf("could not answer %s", id)
	}
	labels, err := showCardLabels(ctx, c.Cards, id)
	if err != nil {
		return fail(err)
	}
	if !slices.Contains(labels, LabelNeedsPrefix+needsKing) {
		return fmt.Sprintf("%s is not waiting on the king", id)
	}
	if err := c.Cards.Comment(ctx, id, "answer from the king ("+via+"):\n"+text); err != nil {
		return fail(err)
	}
	if err := c.Cards.RemoveLabel(ctx, id, LabelNeedsPrefix+needsKing); err != nil {
		return fail(err)
	}
	return "answered " + id
}
