package gemba

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/gitx/gittest"
	"github.com/Fewbytes/sweatshop/line/guidance"
	"github.com/Fewbytes/sweatshop/line/linedir"
	"github.com/Fewbytes/sweatshop/line/maker"
	"github.com/Fewbytes/sweatshop/line/station"
)

// labelledBeads offers, like bd and the cards API, only the cards that carry
// the requested label, and records what it was asked and told.
type labelledBeads struct {
	cards []maker.Bead
	calls []string
}

func (f *labelledBeads) Ready(_ context.Context, label string) ([]maker.Bead, error) {
	f.calls = append(f.calls, "ready "+label)
	var out []maker.Bead
	for _, c := range f.cards {
		if slices.Contains(c.Labels, label) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *labelledBeads) Claim(_ context.Context, id, agent string) error {
	f.calls = append(f.calls, "claim "+id+" "+agent)
	return nil
}

func (f *labelledBeads) Close(_ context.Context, id, _ string) error {
	f.calls = append(f.calls, "close "+id)
	return nil
}

func (f *labelledBeads) Release(_ context.Context, id, _ string) error {
	f.calls = append(f.calls, "release "+id)
	return nil
}

func (f *labelledBeads) CommentCount(context.Context, string) (int, error) { return 0, nil }

func (f *labelledBeads) Claimed(context.Context, string) ([]maker.Bead, error) { return nil, nil }

func (f *labelledBeads) GiveUp(_ context.Context, id, _ string, _ bool) error {
	f.calls = append(f.calls, "giveup "+id)
	return nil
}

type recordingEngine struct{ specs []maker.ContainerSpec }

func (e *recordingEngine) Run(_ context.Context, spec maker.ContainerSpec) (int, error) {
	e.specs = append(e.specs, spec)
	return 0, nil
}

func gembaRunner(t *testing.T, cards *labelledBeads, engine maker.Engine) *maker.Runner {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	bare := gitx.Repo{Dir: filepath.Join(dir, "remote.git")}
	work := gitx.Repo{Dir: filepath.Join(dir, "work")}
	for _, step := range [][]string{
		{"init", "--quiet", "--bare", "--initial-branch=master", bare.Dir},
		{"init", "--quiet", "--initial-branch=master", work.Dir},
	} {
		if _, err := gittest.Git(ctx, dir, step...); err != nil {
			t.Fatal(err)
		}
	}
	for _, step := range [][]string{
		{"config", "user.name", "test"},
		{"config", "user.email", "test@example.com"},
		{"remote", "add", "origin", bare.Dir},
		{"commit", "--quiet", "--allow-empty", "-m", "seed"},
		{"push", "--quiet", "origin", "master"},
	} {
		if _, err := gittest.Git(ctx, work.Dir, step...); err != nil {
			t.Fatal(err)
		}
	}
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &maker.Runner{
		Line:    linedir.Line{Dir: dir, Port: 7468, Remote: bare.Dir, Image: "sweatshop-station:test"},
		Station: linedir.Station{Name: "gemba-1", Role: "gemba", AgentID: "gemba-1", TokenFile: tokenFile},
		Beads:   cards, Engine: engine,
		Log: log.New(&bytes.Buffer{}, "", 0),
	}
	Configure(r)
	return r
}

func TestRunnerClaimsOnlyGembaCards(t *testing.T) {
	cards := &labelledBeads{cards: []maker.Bead{
		{ID: "b-1", Priority: 0, Labels: []string{"line"}},
		{ID: "b-2", Priority: 2, Labels: []string{Label}},
		{ID: "b-3", Priority: 1, Labels: []string{Label, "line"}},
		{ID: "b-4", Priority: 0, Labels: []string{"line", "andon"}},
	}}
	engine := &recordingEngine{}
	worked, err := gembaRunner(t, cards, engine).Once(context.Background())
	if err != nil || !worked {
		t.Fatalf("Once = %v %v", worked, err)
	}
	if want := []string{"ready gemba", "claim b-3 gemba-1", "release b-3"}; !reflect.DeepEqual(cards.calls, want) {
		t.Fatalf("calls = %q, want %q", cards.calls, want)
	}
	if got := engine.specs[0].Env["SWEATSHOP_BEAD"]; got != "b-3" {
		t.Fatalf("container works %q, want b-3", got)
	}
}

func TestRunnerIdlesWithoutGembaCards(t *testing.T) {
	cards := &labelledBeads{cards: []maker.Bead{{ID: "b-1", Labels: []string{"line"}}}}
	engine := &recordingEngine{}
	worked, err := gembaRunner(t, cards, engine).Once(context.Background())
	if err != nil || worked || len(engine.specs) != 0 {
		t.Fatalf("Once = %v %v with %d runs, want idle", worked, err, len(engine.specs))
	}
}

func TestRunnerBriefsAgentWithGembaPrompt(t *testing.T) {
	card := maker.Bead{ID: "b-3", Title: "Cover login failure", Description: "Users see 500.", Labels: []string{Label}}
	cards := &labelledBeads{cards: []maker.Bead{card}}
	engine := &recordingEngine{}
	if _, err := gembaRunner(t, cards, engine).Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := engine.specs[0].Env["SWEATSHOP_PROMPT"]; got != Prompt(card) {
		t.Fatalf("SWEATSHOP_PROMPT is not the gemba prompt:\n%s", got)
	}
}

func TestPromptEncodesMandateAndRule(t *testing.T) {
	p := Prompt(maker.Bead{ID: "b-3", Title: "Cover login failure", Description: "Users see 500."})
	for _, want := range []string{
		"You are a Gemba station", "ID: b-3", "Cover login failure", "Users see 500.",
		"acceptance tests", "add:", "fix:", "retire:", "fail on the pre-fix commit",
		"--label needs:gemba", "Bead: b-3", "sweatshop line merge", "RESULT: merged", "RESULT: gave-up",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	for _, unwanted := range []string{"Maker station", "needs:maker", "Andon-Event"} {
		if strings.Contains(p, unwanted) {
			t.Errorf("prompt mentions %q", unwanted)
		}
	}
}

func TestPromptEndsWithGembaGuidance(t *testing.T) {
	guide, _ := guidance.For("gemba")
	p := Prompt(maker.Bead{ID: "b-3"})
	if !strings.HasSuffix(p, guide+"\n") {
		t.Error("prompt does not end with the writer and gemba guides")
	}
	if strings.Index(p, "RESULT: merged") > strings.Index(p, guide) {
		t.Error("guide precedes the line protocol")
	}
}

func TestLabelIsTheOneAcceptanceRequestsAreFiledUnder(t *testing.T) {
	if Label != station.LabelGemba {
		t.Fatalf("runner claims %q but requests are labelled %q", Label, station.LabelGemba)
	}
}
