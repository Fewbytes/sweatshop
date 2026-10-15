package heijunka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/line/station"
)

// MaxReaps is how many times a card is returned to the queue before the
// reaper hands it to the viceroy instead, so a card that kills its makers
// does not loop forever.
const MaxReaps = 3

// ReapCommentPrefix starts every comment the reaper writes. Reaps are counted
// from these comments, so the count survives a heijunka restart.
const ReapCommentPrefix = "heijunka: reaped"

// ClaimedCard is an in_progress line card as the reaper saw it.
type ClaimedCard struct {
	ID       string
	Assignee string
	Revision string // card revision at read time; Release fails if it moved
	Labels   []string
	Reaps    int // earlier reaps, counted from ReapCommentPrefix comments
}

// ReapCards reads and releases claimed cards.
type ReapCards interface {
	// Claimed lists the line's in_progress cards.
	Claimed(ctx context.Context) ([]ClaimedCard, error)
	// Release clears the card's assignee, reopens it and comments note, only if
	// the card is still at c.Revision; otherwise it returns ErrCardMoved
	// because the holder settled it first.
	Release(ctx context.Context, c ClaimedCard, note string) error
	// Escalate labels the card needs:viceroy and comments note, leaving the
	// claim in place.
	Escalate(ctx context.Context, c ClaimedCard, note string) error
}

// ErrCardMoved is returned by ReapCards.Release when the card changed after it
// was read.
var ErrCardMoved = errors.New("card changed since it was read")

// Reaper returns claims whose holder is dead or busy elsewhere to the queue.
// It acts on the shared resource only; it never commands the agent.
type Reaper struct {
	Cards ReapCards
	Log   *log.Logger
}

// AbandonedReason says why a claim is abandoned, naming the evidence observed.
// It returns "" when the holder is alive and working the card, or has not
// been seen yet. A live holder
// that works another card (or idles) has dropped the claim, so that counts.
func AbandonedReason(c ClaimedCard, stations []station.StationStatus) string {
	i := slices.IndexFunc(stations, func(s station.StationStatus) bool { return s.Name == c.Assignee })
	if i < 0 {
		return fmt.Sprintf("station %s no longer exists", c.Assignee)
	}
	s := stations[i]
	switch {
	case s.LastSeen == nil:
		// Liveness is in memory: after a line restart every station is unseen
		// until its next poll, which says nothing about its health.
		return ""
	case !s.Alive:
		return fmt.Sprintf("station %s silent since %s (last seen)", s.Name, s.LastSeen.UTC().Format(time.RFC3339))
	case s.State != station.StateWorking || s.Card != c.ID:
		return fmt.Sprintf("station %s is %s on card %q, not this one", s.Name, s.State, s.Card)
	}
	return ""
}

// Reap releases every abandoned claim in the status, or labels it needs:viceroy
// once it was reaped MaxReaps times. A card that fails is logged and left for
// the next wake; the errors are joined and returned after the rest were tried.
// A card that moved under the reaper is not an error: its holder settled it.
func (r Reaper) Reap(ctx context.Context, st station.Status) error {
	claimed, err := r.Cards.Claimed(ctx)
	if err != nil {
		return fmt.Errorf("list claimed cards: %w", err)
	}
	var errs []error
	for _, c := range claimed {
		reason := AbandonedReason(c, st.Stations)
		if reason == "" || slices.Contains(c.Labels, station.LabelNeedsPrefix+"viceroy") {
			continue
		}
		note := fmt.Sprintf("%s (reap %d of %d): %s", ReapCommentPrefix, c.Reaps+1, MaxReaps, reason)
		if c.Reaps >= MaxReaps {
			note = fmt.Sprintf("%s: %s; already reaped %d times, so not released again", ReapCommentPrefix, reason, c.Reaps)
			err = r.Cards.Escalate(ctx, c, note)
		} else {
			err = r.Cards.Release(ctx, c, note)
		}
		switch {
		case errors.Is(err, ErrCardMoved):
			r.logf("%s: holder settled first; not reaped", c.ID)
		case err != nil:
			errs = append(errs, fmt.Errorf("reap %s: %w", c.ID, err))
		default:
			r.logf("%s: %s", c.ID, note)
		}
	}
	return errors.Join(errs...)
}

func (r Reaper) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log.Printf(format, args...)
	}
}

// BeadsReap implements ReapCards with bd run in Dir. bd has no conditional
// update, so Release re-reads the card and compares its revision right before
// unclaiming; the window left is one bd call.
type BeadsReap struct {
	BeadsNeeds // bd runner
}

// Claimed implements ReapCards.
func (b BeadsReap) Claimed(ctx context.Context) ([]ClaimedCard, error) {
	out, err := b.bd(ctx, "list", "--label", station.LabelLine, "--status", "in_progress", "--limit", "0", "--json")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("bd list in_progress: parse output: %w", err)
	}
	cards := make([]ClaimedCard, 0, len(rows))
	for _, r := range rows {
		c, err := b.read(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		cards = append(cards, c)
	}
	return cards, nil
}

// read loads one card with its revision and its earlier reap count.
func (b BeadsReap) read(ctx context.Context, id string) (ClaimedCard, error) {
	out, err := b.bd(ctx, "show", id, "--json")
	if err != nil {
		return ClaimedCard{}, err
	}
	var shown []struct {
		Assignee string   `json:"assignee"`
		Revision string   `json:"revision"`
		Labels   []string `json:"labels"`
	}
	if err := json.Unmarshal(out, &shown); err != nil || len(shown) != 1 {
		return ClaimedCard{}, fmt.Errorf("bd show %s: unexpected output (%v)", id, err)
	}
	out, err = b.bd(ctx, "comments", id, "--json")
	if err != nil {
		return ClaimedCard{}, err
	}
	var comments []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(out, &comments); err != nil {
		return ClaimedCard{}, fmt.Errorf("bd comments %s: parse output: %w", id, err)
	}
	reaps := 0
	for _, c := range comments {
		if strings.HasPrefix(c.Text, ReapCommentPrefix) {
			reaps++
		}
	}
	s := shown[0]
	return ClaimedCard{ID: id, Assignee: s.Assignee, Revision: s.Revision, Labels: s.Labels, Reaps: reaps}, nil
}

// Release implements ReapCards. It unclaims as the assignee, which bd requires.
func (b BeadsReap) Release(ctx context.Context, c ClaimedCard, note string) error {
	now, err := b.read(ctx, c.ID)
	if err != nil {
		return err
	}
	if now.Revision != c.Revision || now.Assignee != c.Assignee {
		return ErrCardMoved
	}
	_, err = b.bd(ctx, "unclaim", c.ID, "--reason", note, "--actor", c.Assignee)
	return err
}

// Escalate implements ReapCards.
func (b BeadsReap) Escalate(ctx context.Context, c ClaimedCard, note string) error {
	if _, err := b.bd(ctx, "label", "add", c.ID, station.LabelNeedsPrefix+"viceroy"); err != nil {
		return err
	}
	_, err := b.bd(ctx, "comments", "add", "--", c.ID, note)
	return err
}
