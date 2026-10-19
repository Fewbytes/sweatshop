package andon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/station"
)

type fakeAgent struct {
	prompts []string
	answer  string
	err     error
}

func (a *fakeAgent) Run(_ context.Context, prompt string) (string, error) {
	a.prompts = append(a.prompts, prompt)
	return a.answer, a.err
}

type fakeComments struct{ byCard map[string][]string }

func (c *fakeComments) Comment(_ context.Context, card, text string) error {
	if c.byCard == nil {
		c.byCard = map[string][]string{}
	}
	c.byCard[card] = append(c.byCard[card], text)
	return nil
}

// triagedCards is fakeCards that also triages through an LLMTriage.
type triagedCards struct {
	*fakeCards
	LLMTriage
}

func newTriagedWatcher(api *fakeAPI, agent *fakeAgent) (*Watcher, *fakeComments) {
	w, cards := newWatcher(api)
	comments := &fakeComments{}
	w.Cards = triagedCards{fakeCards: cards, LLMTriage: LLMTriage{
		Agent: agent,
		Evidence: func(_ context.Context, id string) ([]byte, error) {
			return []byte("log of " + id + ": FAIL TestX"), nil
		},
		Comments: comments,
	}}
	return w, comments
}

func TestFailingMasterGetsOneTriageCommentOnItsCard(t *testing.T) {
	api := &fakeAPI{
		status:  station.Status{Master: "bbb"},
		pullRes: guard.Result{Status: guard.Stopped, Event: "ev1"},
	}
	agent := &fakeAgent{answer: "Cause: TestX\nKind: real regression"}
	w, comments := newTriagedWatcher(api, agent)
	step(t, w)
	step(t, w) // the stopped line is re-carded every step; triage must not repeat
	got := comments.byCard["card-new"]
	if len(got) != 1 || !strings.Contains(got[0], "Kind: real regression") {
		t.Fatalf("comments on card-new = %q, want one carrying the agent's answer", got)
	}
	if len(agent.prompts) != 1 || !strings.Contains(agent.prompts[0], "FAIL TestX") {
		t.Fatalf("prompts = %q, want one containing the failing check's log", agent.prompts)
	}
}

func TestTriageFailureDoesNotStopCarding(t *testing.T) {
	api := &fakeAPI{
		status:  station.Status{Master: "bbb"},
		pullRes: guard.Result{Status: guard.Stopped, Event: "ev1"},
	}
	w, comments := newTriagedWatcher(api, &fakeAgent{err: errors.New("model down")})
	step(t, w)
	if api.pulls != 1 || len(comments.byCard) != 0 {
		t.Fatalf("pulls=%d comments=%v, want the cord pulled and no comment", api.pulls, comments.byCard)
	}
}

func TestTriagePromptKeepsTheTailOfALongLog(t *testing.T) {
	long := []byte(strings.Repeat("a", maxEvidenceTail) + "THE-FAILURE")
	p := TriagePrompt(guard.AndonEvent{Check: "ci"}, "m", long)
	if !strings.Contains(p, "THE-FAILURE") || strings.Count(p, "a") > maxEvidenceTail+200 {
		t.Fatalf("prompt did not keep exactly the tail of the log (len %d)", len(p))
	}
}
