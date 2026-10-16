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
	approveCommandName = "approve"
	approveCardOption  = "card"
	approveEmoji       = "✅"
	maxChoices         = 25 // Discord's cap on autocomplete choices
	maxChoiceChars     = 100

	interactionApplicationCommand = 2
	interactionAutocomplete       = 4
	optionTypeString              = 3
	optionTypeInteger             = 4
	callbackChannelMessage        = 4
	callbackAutocompleteResult    = 8
	flagEphemeral                 = 1 << 6
)

// cardTokenRe finds card-id-shaped words in a post; trailing punctuation is
// not part of an id.
var cardTokenRe = regexp.MustCompile(`[a-z0-9]+(?:[-.][a-z0-9]+)*`)

// CommandChoice is one autocomplete suggestion.
type CommandChoice struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type cardSummary struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Labels []string `json:"labels"`
}

func (c *Channel) listCards(ctx context.Context, f ListFilter) ([]cardSummary, error) {
	if c.Cards == nil {
		return nil, nil
	}
	raw, err := c.Cards.List(ctx, f)
	if err != nil {
		return nil, err
	}
	var cards []cardSummary
	if err := json.Unmarshal(raw, &cards); err != nil {
		return nil, fmt.Errorf("decode cards: %w", err)
	}
	return cards, nil
}

// registerSlashCommands makes /approve and /card available in the line's
// guild. A failure is logged: text and reaction approvals still work without
// them.
func (c *Channel) registerSlashCommands(ctx context.Context, app string) {
	guild, err := c.Discord.GuildOf(ctx, c.ChannelID)
	if err != nil {
		log.Printf("channel: register slash commands: %v", err)
		return
	}
	if err := c.Discord.RegisterApproveCommand(ctx, app, guild); err != nil {
		log.Printf("channel: register /approve: %v", err)
	}
	if err := c.Discord.RegisterCardCommand(ctx, app, guild); err != nil {
		log.Printf("channel: register /card: %v", err)
	}
}

// gatewayReaction turns the king's ✅ on a bot post that names exactly one
// card into that card's approval.
func (c *Channel) gatewayReaction(ctx context.Context, raw json.RawMessage) {
	var r struct {
		UserID    string `json:"user_id"`
		ChannelID string `json:"channel_id"`
		MessageID string `json:"message_id"`
		Emoji     struct {
			Name string `json:"name"`
		} `json:"emoji"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		log.Printf("channel: gateway reaction: %v", err)
		return
	}
	if c.King == "" || r.UserID != c.King || r.ChannelID != c.ChannelID || r.Emoji.Name != approveEmoji {
		return
	}
	post, err := c.Discord.Message(ctx, c.ChannelID, r.MessageID)
	if err != nil {
		log.Printf("channel: reaction on %s: %v", r.MessageID, err)
		return
	}
	c.feedMu.Lock()
	botID := c.me
	c.feedMu.Unlock()
	if post.Author.ID != botID {
		return
	}
	cards, err := c.listCards(ctx, ListFilter{})
	if err != nil {
		log.Printf("channel: reaction on %s: %v", r.MessageID, err)
		return
	}
	named := map[string]bool{}
	for _, tok := range cardTokenRe.FindAllString(post.Content, -1) {
		for _, card := range cards {
			if card.ID == tok {
				named[tok] = true
			}
		}
	}
	if len(named) != 1 {
		return
	}
	for id := range named {
		c.reply(ctx, c.approveCard(ctx, id, "discord reaction on message "+r.MessageID))
	}
}

// gatewayInteraction serves /approve, its autocomplete and /card for the king;
// every other user's interaction is ignored.
func (c *Channel) gatewayInteraction(ctx context.Context, raw json.RawMessage) {
	var in struct {
		ID        string `json:"id"`
		Token     string `json:"token"`
		Type      int    `json:"type"`
		ChannelID string `json:"channel_id"`
		Member    struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"member"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Data struct {
			Name    string          `json:"name"`
			Options []commandOption `json:"options"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		log.Printf("channel: gateway interaction: %v", err)
		return
	}
	userID := in.Member.User.ID
	if userID == "" {
		userID = in.User.ID
	}
	if c.King == "" || userID != c.King || in.ChannelID != c.ChannelID {
		return
	}
	switch in.Data.Name {
	case cardCommandName:
		if in.Type != interactionApplicationCommand {
			return
		}
		text := c.fileCard(ctx, in.Data.Options, "discord interaction "+in.ID)
		if err := c.Discord.RespondEphemeral(ctx, in.ID, in.Token, text); err != nil {
			log.Printf("channel: card reply: %v", err)
		}
		return
	case approveCommandName:
	default:
		return
	}
	card := ""
	for _, o := range in.Data.Options {
		if o.Name == approveCardOption {
			card = o.stringValue()
		}
	}
	switch in.Type {
	case interactionAutocomplete:
		choices, err := c.proposedChoices(ctx, card)
		if err != nil {
			log.Printf("channel: autocomplete: %v", err)
			return
		}
		if err := c.Discord.RespondChoices(ctx, in.ID, in.Token, choices); err != nil {
			log.Printf("channel: autocomplete reply: %v", err)
		}
	case interactionApplicationCommand:
		text := "invalid card id " + card
		if cardIDRe.MatchString(card) {
			text = c.approveCard(ctx, card, "discord interaction "+in.ID)
		}
		if err := c.Discord.RespondEphemeral(ctx, in.ID, in.Token, text); err != nil {
			log.Printf("channel: approve reply: %v", err)
		}
	}
}

// proposedChoices suggests line-proposed cards whose id or title contains
// typed. Approval leaves LabelProposed on a card, so cards already carrying
// LabelLine are skipped.
func (c *Channel) proposedChoices(ctx context.Context, typed string) ([]CommandChoice, error) {
	cards, err := c.listCards(ctx, ListFilter{Label: LabelProposed})
	if err != nil {
		return nil, err
	}
	typed = strings.ToLower(typed)
	choices := []CommandChoice{}
	now := time.Now()
	for _, card := range cards {
		if len(choices) == maxChoices {
			break
		}
		if slices.Contains(card.Labels, LabelLine) {
			continue
		}
		name := card.ID + " " + card.Title
		if !strings.Contains(strings.ToLower(name), typed) {
			continue
		}
		if IsDeferred(card.Labels, now) {
			name = "(deferred) " + name
		}
		choices = append(choices, CommandChoice{Name: string([]rune(name)[:min(len([]rune(name)), maxChoiceChars)]), Value: card.ID})
	}
	return choices, nil
}
