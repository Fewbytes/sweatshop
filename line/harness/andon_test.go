package harness

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/Fewbytes/sweatshop/line/andon"
	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/linedir"
	"github.com/Fewbytes/sweatshop/line/station"
)

// recordingCards is a Cards fake that remembers what the watcher raised.
type recordingCards struct{ events []string }

func (c *recordingCards) Raise(_ context.Context, event string, _ guard.AndonEvent) (string, error) {
	c.events = append(c.events, event)
	return "card-" + event, nil
}

// TestAndonStopFixClear walks the whole andon cycle through the real station
// API, witness, guard and watcher: break, stop, fix, clear.
func TestAndonStopFixClear(t *testing.T) {
	ctx := context.Background()

	// A seed repo whose only check fails exactly when BROKEN exists.
	src := gitx.Repo{Dir: t.TempDir()}
	ok(t)(src.Git(ctx, "init", "--quiet", "--initial-branch=master"))
	ok(t)(src.Git(ctx, "config", "user.name", "t"))
	ok(t)(src.Git(ctx, "config", "user.email", "t@example.com"))
	writeFile(t, src.Dir, "check.sh", "test ! -e BROKEN\n")
	ok(t)(src.Git(ctx, "add", "check.sh"))
	ok(t)(src.Git(ctx, "commit", "--quiet", "-m", "seed"))

	l, err := linedir.Init(ctx, linedir.InitOptions{Dir: filepath.Join(t.TempDir(), "line"), Source: src.Dir, Check: "sh check.sh"})
	if err != nil {
		t.Fatal(err)
	}
	// Host-local checks: trust level none, keeping the generated keys and roles.
	c, _, err := constitution.Load(l.Constitution)
	if err != nil {
		t.Fatal(err)
	}
	c.Trust.Level = "none"
	c.Runner = constitution.Runner{Kind: "local"}
	data, err := toml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Constitution, data, 0o644); err != nil {
		t.Fatal(err)
	}

	srv, err := station.NewFromLine(l)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	var maker linedir.Station
	tokens := map[string]string{}
	for _, s := range l.Stations {
		tok, err := s.Token()
		if err != nil {
			t.Fatal(err)
		}
		tokens[s.Name] = tok
		if s.Name == "maker-1" {
			maker = s
		}
	}
	makerAPI := station.Client{URL: ts.URL, Token: tokens["maker-1"]}
	andonAPI := station.Client{URL: ts.URL, Token: tokens["andon-1"]}

	clone := gitx.Repo{Dir: filepath.Join(t.TempDir(), "maker")}
	ok(t)(src.Git(ctx, "clone", "--quiet", l.Remote, clone.Dir))
	ok(t)(clone.Git(ctx, "config", "user.name", "t"))
	ok(t)(clone.Git(ctx, "config", "user.email", "t@example.com"))
	commit := func(name, content, msg string) string {
		t.Helper()
		writeFile(t, clone.Dir, name, content)
		ok(t)(clone.Git(ctx, "add", "-A"))
		ok(t)(clone.Git(ctx, "commit", "--quiet", "-m", msg))
		sha, err := clone.Commit(ctx, "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		return sha
	}
	master := func() string {
		t.Helper()
		sha, err := (gitx.Repo{Dir: l.Remote}).Commit(ctx, "refs/heads/master")
		if err != nil {
			t.Fatal(err)
		}
		return sha
	}
	status := func() station.Status {
		t.Helper()
		st, err := andonAPI.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}

	cards := &recordingCards{}
	w := &andon.Watcher{API: andonAPI, Cards: cards}

	// Green master: nothing to do.
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if st := status(); st.Event != "" || len(cards.events) != 0 {
		t.Fatalf("green master: status %+v, cards %v", st, cards.events)
	}

	// Break master. The witness never signs a passing run of a broken tree, so
	// the break lands directly through the guard with a passing envelope signed
	// by the maker key, pushed as a candidate first. This models a check that
	// passed pre-merge but fails on master (flakiness, environment drift).
	broken := commit("BROKEN", "x", "break")
	ok(t)(clone.Git(ctx, "push", "--quiet", "origin", broken+":"+guard.CandidatePrefix+broken))
	key, err := attest.LoadPrivateKey(maker.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := clone.Tree(ctx, broken)
	if err != nil {
		t.Fatal(err)
	}
	stmt := attest.NewStatement(broken, tree, attest.ChecksPredicate{
		Role: "maker", AgentID: "maker-1", Constitution: srv.Guard.ConstitutionDigest, Runner: "local",
		Checks: []attest.CheckResult{{Name: "ci", Command: "sh check.sh", ExitCode: 0}},
	})
	env, err := attest.SignStatement(key, stmt)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := srv.Guard.Merge(ctx, broken, env); err != nil || res.Status != guard.Merged {
		t.Fatalf("landing the break = %+v, %v", res, err)
	}
	if master() != broken {
		t.Fatalf("master = %s, want %s", master(), broken)
	}

	// The watcher notices, stops the line and files one card.
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	st := status()
	if st.Event == "" || st.Andon == nil || st.Andon.Commit != broken {
		t.Fatalf("after break: status %+v", st)
	}
	if len(cards.events) != 1 || cards.events[0] != st.Event {
		t.Fatalf("cards = %v, event %q", cards.events, st.Event)
	}
	event := st.Event

	// Normal work is refused while the line is stopped.
	ok(t)(clone.Git(ctx, "rm", "--quiet", "BROKEN"))
	ok(t)(clone.Git(ctx, "commit", "--quiet", "-m", "untagged fix"))
	res, err := makerAPI.Merge(ctx, clone.Dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if res.Guard.Reason != guard.ReasonLineStopped {
		t.Fatalf("untagged merge while stopped = %+v", res)
	}

	// The same fix tagged with the event merges.
	ok(t)(clone.Git(ctx, "commit", "--quiet", "--amend", "-m", "fix\n\n"+guard.FixTrailer+": "+event))
	fix, err := clone.Commit(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	res, err = makerAPI.Merge(ctx, clone.Dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if res.Guard.Status != guard.Merged || master() != fix {
		t.Fatalf("tagged fix = %+v", res)
	}

	// The watcher attests the fixed master and clears the cord.
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if st := status(); st.Event != "" || st.Andon != nil {
		t.Fatalf("after fix: status %+v", st)
	}
	if len(cards.events) != 1 {
		t.Fatalf("cards = %v", cards.events)
	}

	// The line is running again.
	next := commit("src/next.txt", "n", "next change")
	res, err = makerAPI.Merge(ctx, clone.Dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if res.Guard.Status != guard.Merged || master() != next {
		t.Fatalf("change after clear = %+v", res)
	}
}
