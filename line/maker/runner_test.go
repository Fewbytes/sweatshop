package maker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/gitx/gittest"
	"github.com/Fewbytes/sweatshop/line/guidance"
	"github.com/Fewbytes/sweatshop/line/internal/testwait"
	"github.com/Fewbytes/sweatshop/line/linedir"
	"github.com/Fewbytes/sweatshop/line/station"
)

func ok(t *testing.T) func(string, error) {
	return func(_ string, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
}

// remote returns a bare repository whose master holds one seed commit, and
// a work clone that can push to it.
func remote(t *testing.T) (bare, work gitx.Repo) {
	t.Helper()
	ctx := context.Background()
	bare = gitx.Repo{Dir: filepath.Join(t.TempDir(), "remote.git")}
	ok(t)(gittest.Git(ctx, t.TempDir(), "init", "--quiet", "--bare", "--initial-branch=master", bare.Dir))
	work = gitx.Repo{Dir: t.TempDir()}
	ok(t)(gittest.Git(ctx, work.Dir, "init", "--quiet", "--initial-branch=master"))
	ok(t)(gittest.Git(ctx, work.Dir, "config", "user.name", "test"))
	ok(t)(gittest.Git(ctx, work.Dir, "config", "user.email", "test@example.com"))
	ok(t)(gittest.Git(ctx, work.Dir, "remote", "add", "origin", bare.Dir))
	commit(t, work, "seed")
	return bare, work
}

// commit makes an empty commit with message msg and pushes it to master.
func commit(t *testing.T, work gitx.Repo, msg string) {
	t.Helper()
	ctx := context.Background()
	ok(t)(gittest.Git(ctx, work.Dir, "commit", "--quiet", "--allow-empty", "-m", msg))
	ok(t)(gittest.Git(ctx, work.Dir, "push", "--quiet", "origin", "master"))
}

func TestPickOrdering(t *testing.T) {
	beads := []Bead{
		{ID: "b-3", Priority: 2},
		{ID: "b-2", Priority: 1},
		{ID: "b-1", Priority: 1},
		{ID: "b-9", Priority: 3, Labels: []string{"line", "andon"}},
		{ID: "b-0", Priority: 4},
	}
	got, found := Pick(beads, false)
	if !found || got.ID != "b-9" {
		t.Fatalf("Pick = %v %v, want andon card b-9 first", got.ID, found)
	}
	got, _ = Pick(beads[:3], false)
	if got.ID != "b-1" {
		t.Fatalf("Pick = %v, want b-1 (lowest priority number, then ID)", got.ID)
	}
	if _, found := Pick(nil, false); found {
		t.Fatal("Pick(nil) found a bead")
	}
}

func TestPickSkipsGaveUpAndOnlyAndonWhileStopped(t *testing.T) {
	beads := []Bead{
		{ID: "b-1", Priority: 0, Labels: []string{"line", GaveUpLabel}},
		{ID: "b-2", Priority: 1, Labels: []string{"line"}},
		{ID: "b-3", Priority: 2, Labels: []string{"line", "andon", GaveUpLabel}},
	}
	if got, found := Pick(beads, false); !found || got.ID != "b-2" {
		t.Fatalf("Pick = %v %v, want b-2 (gave-up beads skipped)", got.ID, found)
	}
	if got, found := Pick(beads, true); found {
		t.Fatalf("Pick while stopped = %v, want nothing (no live andon card)", got.ID)
	}
	beads = append(beads, Bead{ID: "b-4", Priority: 3, Labels: []string{"line", "andon"}})
	if got, found := Pick(beads, true); !found || got.ID != "b-4" {
		t.Fatalf("Pick while stopped = %v %v, want andon card b-4", got.ID, found)
	}
}

func head(t *testing.T, bare gitx.Repo) string {
	t.Helper()
	sha, err := bare.Ref(context.Background(), "refs/heads/master")
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

func TestLanded(t *testing.T) {
	ctx := context.Background()
	bare, work := remote(t)
	pre := head(t, bare)
	if landed, err := Landed(ctx, bare, "b-7", pre); err != nil || landed {
		t.Fatalf("Landed before = %v %v, want false", landed, err)
	}
	commit(t, work, "unrelated\n\nBead: b-70")
	commit(t, work, "do the thing\n\nBead: b-7\nAndon-Event: e1")
	commit(t, work, "later")
	if landed, err := Landed(ctx, bare, "b-7", pre); err != nil || !landed {
		t.Fatalf("Landed after = %v %v, want true", landed, err)
	}
	if landed, err := Landed(ctx, bare, "b-8", pre); err != nil || landed {
		t.Fatalf("Landed(b-8) = %v %v, want false", landed, err)
	}
	// Only commits added since pre count: an older trailer (another card's
	// commit claiming this bead, or an earlier partial land) does not.
	if landed, err := Landed(ctx, bare, "b-7", head(t, bare)); err != nil || landed {
		t.Fatalf("Landed with pre at tip = %v %v, want false", landed, err)
	}
}

// I4: a Bead trailer planted on master before the run must not close the bead.
func TestOncePlantedTrailerDoesNotClose(t *testing.T) {
	bare, work := remote(t)
	commit(t, work, "other card\n\nBead: b-1")
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Priority: 1}}}
	r := newRunner(t, bare, fb, &fakeEngine{})
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if last := fb.calls[len(fb.calls)-1]; !strings.HasPrefix(last, "release b-1 ") {
		t.Fatalf("last call = %q, want a release", last)
	}
}

// I3: a bead that never lands is given up after MaxAttempts runs and not
// picked again.
func TestOnceGivesUpAfterMaxAttempts(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Priority: 1, Labels: []string{"line"}}, {ID: "b-2", Priority: 2, Labels: []string{"line"}}}}
	fe := &fakeEngine{run: func(ContainerSpec) (int, error) { return 3, nil }}
	r := newRunner(t, bare, fb, fe)
	ctx := context.Background()
	for i := 0; i < DefaultMaxAttempts; i++ {
		if _, err := r.Once(ctx); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		"ready line", "claim b-1 maker-1", "release b-1 maker run ended without landing (exit 3) [attempt 1/3]" + noReportNote,
		"ready line", "claim b-1 maker-1", "release b-1 maker run ended without landing (exit 3) [attempt 2/3]" + noReportNote,
		"ready line", "claim b-1 maker-1", "release b-1 maker run ended without landing (exit 3) [attempt 3/3]; giving up: labelled " + GaveUpLabel + noReportNote,
		"giveup b-1",
	}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Fatalf("calls =\n%q\nwant\n%q", fb.calls, want)
	}
	fb.calls = nil
	if _, err := r.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if fb.calls[1] != "claim b-2 maker-1" {
		t.Fatalf("after giving up on b-1: calls = %q", fb.calls)
	}
}

func TestOnceEscalatesGivenUpAndonCardToKing(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{
		{ID: "b-1", Priority: 1, Labels: []string{"line"}},
		{ID: "b-9", Priority: 3, Labels: []string{"line", "andon"}, Description: "event: e1"},
	}}
	fe := &fakeEngine{run: func(ContainerSpec) (int, error) { return 3, nil }}
	r := newRunner(t, bare, fb, fe)
	r.MaxAttempts = 1
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if last := fb.calls[len(fb.calls)-1]; last != "giveup b-9 needs:king" {
		t.Fatalf("last call = %q, want giveup of the andon card escalated to the king", last)
	}
}

func TestOnceMaxAttemptsConfigurable(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Priority: 1, Labels: []string{"line"}}}}
	r := newRunner(t, bare, fb, &fakeEngine{run: func(ContainerSpec) (int, error) { return 1, nil }})
	r.MaxAttempts = 1
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if last := fb.calls[len(fb.calls)-1]; last != "giveup b-1" {
		t.Fatalf("calls = %q", fb.calls)
	}
}

// I3: while refs/line/andon exists only andon cards are worked.
func TestOnceWhileStoppedWorksOnlyAndonCards(t *testing.T) {
	bare, _ := remote(t)
	ok(t)(gittest.Git(context.Background(), bare.Dir, "update-ref", "refs/line/andon", head(t, bare)))
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Priority: 0, Labels: []string{"line"}}}}
	fe := &fakeEngine{}
	r := newRunner(t, bare, fb, fe)
	worked, err := r.Once(context.Background())
	if err != nil || worked || len(fe.specs) != 0 {
		t.Fatalf("Once while stopped = %v %v runs=%d calls=%q, want idle", worked, err, len(fe.specs), fb.calls)
	}
	fb.ready = append(fb.ready, Bead{ID: "b-9", Priority: 3, Labels: []string{"line", "andon"}, Description: "event: e1"})
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fe.specs) != 1 || fe.specs[0].Env["SWEATSHOP_BEAD"] != "b-9" {
		t.Fatalf("specs = %+v", fe.specs)
	}
}

type fakeBeads struct {
	ready  []Bead
	calls  []string
	claims map[string]string
	// comments is what CommentCount reports; a run may raise it to act as an
	// agent that posted a failure report.
	comments int
	// stranded is what Claimed reports.
	stranded []Bead
	// failRelease and failClose are how many calls of each fail before one succeeds.
	failRelease, failClose int
}

// noReportNote is what a release note ends with when the run left no
// comment and printed nothing.
const noReportNote = "; the maker left no failure report and no output"

func (f *fakeBeads) CommentCount(context.Context, string) (int, error) { return f.comments, nil }

func (f *fakeBeads) GiveUp(_ context.Context, id, reason string, escalateToKing bool) error {
	f.calls = append(f.calls, "giveup "+id)
	if escalateToKing {
		f.calls[len(f.calls)-1] += " needs:king"
	}
	for i := range f.ready {
		if f.ready[i].ID == id {
			f.ready[i].Labels = append(f.ready[i].Labels, GaveUpLabel)
		}
	}
	return nil
}

func (f *fakeBeads) Ready(_ context.Context, label string) ([]Bead, error) {
	f.calls = append(f.calls, "ready "+label)
	return f.ready, nil
}

func (f *fakeBeads) Claim(_ context.Context, id, agent string) error {
	f.calls = append(f.calls, "claim "+id+" "+agent)
	return nil
}

func (f *fakeBeads) Close(_ context.Context, id, reason string) error {
	f.calls = append(f.calls, "close "+id+" "+reason)
	if f.failClose > 0 {
		f.failClose--
		return errors.New("close failed")
	}
	return nil
}

func (f *fakeBeads) Release(_ context.Context, id, note string) error {
	f.calls = append(f.calls, "release "+id+" "+note)
	if f.failRelease > 0 {
		f.failRelease--
		return errors.New("release failed")
	}
	return nil
}

func (f *fakeBeads) Claimed(_ context.Context, agent string) ([]Bead, error) {
	stranded := f.stranded
	f.stranded = nil
	return stranded, nil
}

type fakeEngine struct {
	specs []ContainerSpec
	run   func(spec ContainerSpec) (int, error)
	block bool // run until the context ends

	started chan struct{} // if non-nil, closed when Run begins
}

func (f *fakeEngine) Run(ctx context.Context, spec ContainerSpec) (int, error) {
	f.specs = append(f.specs, spec)
	if f.started != nil {
		close(f.started)
	}
	if f.block {
		<-ctx.Done()
		return 143, nil
	}
	if f.run == nil {
		return 0, nil
	}
	return f.run(spec)
}

func newRunner(t *testing.T, bare gitx.Repo, b Beads, e Engine) *Runner {
	t.Helper()
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("tok-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Runner{
		Line:    linedir.Line{Dir: dir, Port: 7468, Remote: bare.Dir, Image: "sweatshop-station:test"},
		Station: linedir.Station{Name: "maker-1", Role: "maker", AgentID: "maker-1", TokenFile: tokenFile},
		Beads:   b, Engine: e,
		Log: log.New(&bytes.Buffer{}, "", 0),
	}
}

func TestOnceClosesLandedBead(t *testing.T) {
	bare, work := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-2", Title: "two", Priority: 2}, {ID: "b-1", Title: "one", Priority: 1}}}
	fe := &fakeEngine{run: func(spec ContainerSpec) (int, error) {
		commit(t, work, "work\n\nBead: "+spec.Env["SWEATSHOP_BEAD"])
		return 0, nil
	}}
	r := newRunner(t, bare, fb, fe)
	worked, err := r.Once(context.Background())
	if err != nil || !worked {
		t.Fatalf("Once = %v %v", worked, err)
	}
	want := []string{"ready line", "claim b-1 maker-1", "close b-1 landed via line"}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Fatalf("calls = %q, want %q", fb.calls, want)
	}
}

func TestOnceReleasesUnlandedBead(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Title: "one", Priority: 1}}}
	fe := &fakeEngine{run: func(ContainerSpec) (int, error) { return 3, nil }}
	r := newRunner(t, bare, fb, fe)
	worked, err := r.Once(context.Background())
	if err != nil || !worked {
		t.Fatalf("Once = %v %v", worked, err)
	}
	want := []string{"ready line", "claim b-1 maker-1", "release b-1 maker run ended without landing (exit 3) [attempt 1/3]" + noReportNote}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Fatalf("calls = %q, want %q", fb.calls, want)
	}
}

func TestOnceReleasesWhenEngineFails(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Priority: 1}}}
	fe := &fakeEngine{run: func(ContainerSpec) (int, error) { return -1, errors.New("no engine") }}
	r := newRunner(t, bare, fb, fe)
	if _, err := r.Once(context.Background()); err == nil {
		t.Fatal("Once hid the engine failure")
	}
	if last := fb.calls[len(fb.calls)-1]; !strings.HasPrefix(last, "release b-1 ") {
		t.Fatalf("last call = %q, want a release", last)
	}
}

func TestOnceTimesOut(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Priority: 1}}}
	r := newRunner(t, bare, fb, &fakeEngine{block: true})
	r.Timeout = 50 * time.Millisecond
	worked, err := r.Once(context.Background())
	if err != nil || !worked {
		t.Fatalf("Once = %v %v", worked, err)
	}
	if last := fb.calls[len(fb.calls)-1]; !strings.HasPrefix(last, "release b-1 ") || !strings.Contains(last, "timed out after 50ms") {
		t.Fatalf("last call = %q, want a timed-out release", last)
	}
}

func TestOnceStopsRunAndReleasesWithoutAttemptWhenCordPulled(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Priority: 1, Labels: []string{"line"}}}}
	fe := &fakeEngine{block: true, started: make(chan struct{})}
	r := newRunner(t, bare, fb, fe)
	r.StopPoll = 10 * time.Millisecond
	go func() {
		<-fe.started
		ok(t)(gittest.Git(context.Background(), bare.Dir, "update-ref", "refs/line/andon", head(t, bare)))
	}()
	worked, err := r.Once(context.Background())
	if err != nil || !worked {
		t.Fatalf("Once = %v %v", worked, err)
	}
	want := []string{"ready line", "claim b-1 maker-1", "release b-1 maker run stopped: andon cord pulled (exit 143)"}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Fatalf("calls = %q, want %q (no attempt counted)", fb.calls, want)
	}
	if r.attempts["b-1"] != 0 {
		t.Fatalf("attempts = %v, want none counted", r.attempts)
	}
}

func TestOnceLetsAndonCardRunWhileCordPulled(t *testing.T) {
	bare, _ := remote(t)
	ok(t)(gittest.Git(context.Background(), bare.Dir, "update-ref", "refs/line/andon", head(t, bare)))
	fb := &fakeBeads{ready: []Bead{{ID: "b-9", Priority: 0, Labels: []string{"line", "andon"}, Description: "event: e1"}}}
	r := newRunner(t, bare, fb, &fakeEngine{block: true})
	r.StopPoll = 10 * time.Millisecond
	r.Timeout = 100 * time.Millisecond
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if last := fb.calls[len(fb.calls)-1]; !strings.Contains(last, "timed out after 100ms") {
		t.Fatalf("last call = %q, want the andon run to continue until timeout", last)
	}
}

func TestOnceEngineExit125IsAnError(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Priority: 1}}}
	fe := &fakeEngine{run: func(ContainerSpec) (int, error) { return EngineErrorExit, nil }}
	worked, err := newRunner(t, bare, fb, fe).Once(context.Background())
	if err == nil || !worked {
		t.Fatalf("Once = %v %v, want an engine error", worked, err)
	}
	if last := fb.calls[len(fb.calls)-1]; last != "release b-1 maker run ended without landing: container engine error (exit 125)" {
		t.Fatalf("last call = %q", last)
	}
}

func TestOnceIdle(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{}
	fe := &fakeEngine{}
	worked, err := newRunner(t, bare, fb, fe).Once(context.Background())
	if err != nil || worked || len(fe.specs) != 0 {
		t.Fatalf("Once = %v %v with %d runs, want idle", worked, err, len(fe.specs))
	}
}

func TestOnceJoinsLineNetwork(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Title: "one", Priority: 1}}}
	fe := &fakeEngine{}
	r := newRunner(t, bare, fb, fe)
	r.Line.Network = "sweatshop-a"
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fe.specs[0].Network; got != "sweatshop-a" {
		t.Fatalf("station container network = %q, want sweatshop-a", got)
	}
}

func TestOnceContainerSpec(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Title: "one", Priority: 1}}}
	fe := &fakeEngine{}
	r := newRunner(t, bare, fb, fe)
	r.Creds = HostCreds(func(string) (string, bool) { return "", false })
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	spec := fe.specs[0]
	if spec.Image != "sweatshop-station:test" || len(spec.Args) != 0 {
		t.Fatalf("spec = %+v", spec)
	}
	if spec.Env["SWEATSHOP_LINE_TOKEN"] != "tok-123" || spec.Env["SWEATSHOP_LINE_URL"] != "http://host.containers.internal:7468" {
		t.Fatalf("env = %v", spec.Env)
	}
	if spec.Env["SWEATSHOP_BEAD"] != "b-1" || spec.Env["SWEATSHOP_AGENT_ID"] != "maker-1" || !strings.Contains(spec.Env["SWEATSHOP_PROMPT"], "Bead: b-1") {
		t.Fatalf("env = %v", spec.Env)
	}
	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if _, has := spec.Env[k]; has {
			t.Fatalf("env carries %s although the host has none", k)
		}
	}
	want := []Mount{{Host: bare.Dir, Container: "/line/remote.git", ReadOnly: true}}
	if !reflect.DeepEqual(spec.Mounts, want) {
		t.Fatalf("mounts = %+v, want %+v", spec.Mounts, want)
	}

	r.URL = "http://example.test:9"
	r.Creds = HostCreds(func(k string) (string, bool) { return "secret-" + k, k == "ANTHROPIC_API_KEY" })
	fb.ready = []Bead{{ID: "b-2"}}
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	spec = fe.specs[1]
	if spec.Env["ANTHROPIC_API_KEY"] != "secret-ANTHROPIC_API_KEY" || spec.Env["SWEATSHOP_LINE_URL"] != "http://example.test:9" {
		t.Fatalf("env = %v", spec.Env)
	}
	if _, has := spec.Env["CLAUDE_CODE_OAUTH_TOKEN"]; has {
		t.Fatal("unset credential passed through")
	}
}

func TestPrompt(t *testing.T) {
	plain := Prompt(Bead{ID: "b-1", Title: "Add a flag", Description: "Make it so."}, "")
	for _, want := range []string{"b-1", "Add a flag", "Make it so.", "Bead: b-1", "/work", "just ci", "sweatshop line merge", "sweatshop line cards show", "sweatshop line cards list", "--label needs:maker", "sweatshop line cards comment", "base_moved", "checks_failed", "RESULT: merged", "RESULT: gave-up"} {
		if !strings.Contains(plain, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(plain, "Andon-Event") {
		t.Error("plain card prompt mentions Andon-Event")
	}

	card := Bead{ID: "b-2", Title: "andon", Labels: []string{"line", "andon"}, Description: "pulled\n\nevent: 0123abcd\ncommit: ff"}
	ev := AndonEvent(card)
	if ev != "0123abcd" {
		t.Fatalf("AndonEvent = %q", ev)
	}
	if p := Prompt(card, ev); !strings.Contains(p, "Andon-Event: 0123abcd") || !strings.Contains(p, "Bead: b-2") {
		t.Fatalf("andon prompt lacks trailers:\n%s", p)
	}
	if AndonEvent(Bead{Description: "event: x"}) != "" {
		t.Fatal("AndonEvent parsed a card without the andon label")
	}
}

func TestPromptCarriesWriterGuide(t *testing.T) {
	plain := Prompt(Bead{ID: "b-1", Title: "Add a flag", Description: "Make it so."}, "")
	if !strings.Contains(plain, guidance.Writer) {
		t.Error("maker prompt lacks the writer guide")
	}
	andon := Bead{ID: "b-2", Title: "fix master", Labels: []string{"andon"}, Description: "event: 0123abcd"}
	if p := Prompt(andon, "0123abcd"); !strings.Contains(p, guidance.Writer) {
		t.Error("andon prompt lacks the writer guide")
	}
	// The line protocol, including the RESULT contract, stays ahead of the guide.
	if strings.Index(plain, "RESULT: merged") > strings.Index(plain, guidance.Writer) {
		t.Error("guide precedes the line protocol")
	}
}

// fakeBin installs an executable script named name on PATH that appends its
// arguments (one per line, then a blank line) to the returned log file.
func fakeBin(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " + logFile + "; done\necho >> " + logFile + "\n" + body
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logFile
}

func calls(t *testing.T, logFile string) []string {
	t.Helper()
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range strings.Split(strings.TrimRight(string(data), "\n"), "\n\n") {
		out = append(out, strings.ReplaceAll(c, "\n", " "))
	}
	return out
}

func TestBeadsCLI(t *testing.T) {
	logFile := fakeBin(t, "bd", `case "$1" in ready) echo '[{"id":"b-1","title":"t","description":"d","priority":2,"labels":["line"]}]';; esac
`)
	ctx := context.Background()
	b := BeadsCLI{Dir: t.TempDir(), Actor: "maker-1"}
	got, err := b.Ready(ctx, "line")
	if err != nil {
		t.Fatal(err)
	}
	if want := []Bead{{ID: "b-1", Title: "t", Description: "d", Priority: 2, Labels: []string{"line"}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Ready = %+v, want %+v", got, want)
	}
	if err := b.Claim(ctx, "b-1", "maker-1"); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(ctx, "b-1", "landed via line"); err != nil {
		t.Fatal(err)
	}
	if err := b.Release(ctx, "b-1", "no luck"); err != nil {
		t.Fatal(err)
	}
	if err := b.GiveUp(ctx, "b-1", "out of ideas", true); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ready --label line --exclude-label " + GaveUpLabel + " --limit 0 --json",
		"update b-1 --claim --actor maker-1",
		"close b-1 --reason landed via line --actor maker-1",
		"unclaim b-1 --reason no luck --actor maker-1",
		"comments add -- b-1 gave up: out of ideas --actor maker-1",
		"label add b-1 " + GaveUpLabel + " --actor maker-1",
		"label add b-1 needs:king --actor maker-1",
	}
	if got := calls(t, logFile); !reflect.DeepEqual(got, want) {
		t.Fatalf("bd calls =\n%q\nwant\n%q", got, want)
	}
	if err := (BeadsCLI{Dir: t.TempDir()}).Claim(ctx, "-rf", "maker-1"); err == nil {
		t.Fatal("Claim accepted an option-like ID")
	}
}

func TestDockerEngine(t *testing.T) {
	logFile := fakeBin(t, "docker", `case "$1" in run) printf '%s' "$SWEATSHOP_LINE_TOKEN" > "$(dirname "$0")/token.seen"; exit 4;; esac
`)
	e := DockerEngine{Bin: "docker"}
	code, err := e.Run(context.Background(), ContainerSpec{
		Image: "img:1", Name: "maker-1-b-1",
		Env:    map[string]string{"SWEATSHOP_LINE_TOKEN": "tok", "A": "1"},
		Mounts: []Mount{{Host: "/h/remote.git", Container: "/line/remote.git", ReadOnly: true}, {Host: "/h/x", Container: "/x"}},
	})
	if err != nil || code != 4 {
		t.Fatalf("Run = %d %v, want exit 4", code, err)
	}
	want := []string{
		"run --rm --cap-drop=ALL --security-opt=no-new-privileges --name maker-1-b-1 -e A -e SWEATSHOP_LINE_TOKEN -v /h/remote.git:/line/remote.git:ro -v /h/x:/x img:1",
		"rm -f -- maker-1-b-1",
	}
	if got := calls(t, logFile); !reflect.DeepEqual(got, want) {
		t.Fatalf("docker calls = %q, want %q", got, want)
	}
	// Values travel in the engine's environment, never on its command line,
	// so other local users cannot read the token from the process table.
	seen, err := os.ReadFile(filepath.Join(filepath.Dir(logFile), "token.seen"))
	if err != nil || string(seen) != "tok" {
		t.Fatalf("token seen by engine = %q %v", seen, err)
	}
	// Limits reach the engine as flags.
	if _, err := e.Run(context.Background(), ContainerSpec{Image: "img", Memory: "4g", Pids: 1024, CPUs: "2"}); err != nil {
		t.Fatal(err)
	}
	got := calls(t, logFile)
	if want := "run --rm --cap-drop=ALL --security-opt=no-new-privileges --memory 4g --pids-limit 1024 --cpus 2 img"; got[len(got)-1] != want {
		t.Fatalf("docker call = %q, want %q", got[len(got)-1], want)
	}
	// A line's stations join the line's own network, not a bridge shared with other lines.
	if _, err := e.Run(context.Background(), ContainerSpec{Image: "img", Network: "line-a"}); err != nil {
		t.Fatal(err)
	}
	got = calls(t, logFile)
	if want := "run --rm --cap-drop=ALL --security-opt=no-new-privileges --network line-a img"; got[len(got)-1] != want {
		t.Fatalf("docker call = %q, want %q", got[len(got)-1], want)
	}
	if _, err := e.Run(context.Background(), ContainerSpec{Image: "img", Mounts: []Mount{{Host: "/a:b", Container: "/c"}}}); err == nil {
		t.Fatal("Run accepted a mount path containing ':'")
	}
}

// Cancelling a run must stop the CLI gracefully and then remove the
// container, so no orphan keeps working a card that is being released.
func TestDockerEngineCancelRemovesContainer(t *testing.T) {
	dir := t.TempDir()
	never := filepath.Join(dir, "never-written")
	if err := syscall.Mkfifo(never, 0o600); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(dir, "started")
	logFile := fakeBin(t, "docker", `case "$1" in run) touch '`+started+`'; exec cat '`+never+`';; esac
`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		testwait.Until(t, 5*time.Second, "docker run to start", func() bool {
			_, err := os.Stat(started)
			return err == nil
		})
		cancel()
	}()
	if _, err := (DockerEngine{Bin: "docker"}).Run(ctx, ContainerSpec{Image: "img", Name: "maker-1-b-1"}); err == nil {
		t.Fatal("Run of a SIGTERMed CLI reported success")
	}
	got := calls(t, logFile)
	if len(got) != 2 || got[1] != "rm -f -- maker-1-b-1" {
		t.Fatalf("docker calls = %q, want run then rm -f", got)
	}
}

type fakeHeartbeats struct {
	mu   sync.Mutex
	beat []station.Heartbeat
}

func (f *fakeHeartbeats) Heartbeat(_ context.Context, hb station.Heartbeat) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beat = append(f.beat, hb)
	return nil
}

func TestOnceHeartbeatsIdleThenWorkingTheCard(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Priority: 1}}}
	hb := &fakeHeartbeats{}
	r := newRunner(t, bare, fb, &fakeEngine{})
	r.Heartbeats, r.Commit = hb, "abc123"
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []station.Heartbeat{
		{State: station.StateIdle, Commit: "abc123"},
		{State: station.StateWorking, Card: "b-1", Commit: "abc123"},
	}
	if !reflect.DeepEqual(hb.beat, want) {
		t.Fatalf("heartbeats %+v, want %+v", hb.beat, want)
	}
}

func TestOnceHeartbeatsWhyItIsIdle(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Labels: []string{GaveUpLabel}}}}
	hb := &fakeHeartbeats{}
	r := newRunner(t, bare, fb, &fakeEngine{})
	r.Heartbeats = hb
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	last := hb.beat[len(hb.beat)-1]
	if want := "all 1 ready cards are labelled " + GaveUpLabel; last.State != station.StateIdle || last.IdleReason != want {
		t.Fatalf("last heartbeat %+v, want idle with reason %q", last, want)
	}
}

func TestIdleReasonNamesTheCause(t *testing.T) {
	ready := []Bead{{ID: "b-1"}}
	if got := idleReason(nil, false, "line"); got != "no ready cards labelled line" {
		t.Errorf("empty: %q", got)
	}
	if got := idleReason(ready, true, "line"); got != "andon cord pulled and none of 1 ready cards is an andon card" {
		t.Errorf("stopped: %q", got)
	}
}

func TestOnceSignalLogsExitAndReleasesClaim(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Priority: 1}}}
	fe := &fakeEngine{block: true, started: make(chan struct{})}
	r := newRunner(t, bare, fb, fe)
	var logs bytes.Buffer
	r.Log = log.New(&logs, "", 0)
	ctx, cancel := context.WithCancelCause(context.Background())
	go func() {
		<-fe.started
		cancel(SignalCause(syscall.SIGTERM))
	}()
	if err := r.Loop(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	if last := fb.calls[len(fb.calls)-1]; !strings.HasPrefix(last, "release b-1 ") || !strings.Contains(last, "interrupted by terminated") {
		t.Fatalf("last call = %q, want a release naming the signal", last)
	}
	if want := "exiting on terminated: releasing b-1"; !strings.Contains(logs.String(), want) {
		t.Fatalf("log = %q, want %q", logs.String(), want)
	}
}

func TestLoopLogsSignalWhenIdle(t *testing.T) {
	bare, _ := remote(t)
	r := newRunner(t, bare, &fakeBeads{}, &fakeEngine{})
	var logs bytes.Buffer
	r.Log = log.New(&logs, "", 0)
	ctx, cancel := context.WithCancelCause(context.Background())
	time.AfterFunc(50*time.Millisecond, func() { cancel(SignalCause(syscall.SIGINT)) })
	if err := r.Loop(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	if want := "exiting on interrupt\n"; !strings.Contains(logs.String(), want) {
		t.Fatalf("log = %q, want %q", logs.String(), want)
	}
}

func TestLoopWaitsBeforeRetryingCardThatDidNotLand(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Priority: 1}}}
	fe := &fakeEngine{run: func(ContainerSpec) (int, error) { return 128, nil }}
	r := newRunner(t, bare, fb, fe)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(200*time.Millisecond, cancel)
	if err := r.Loop(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	if len(fe.specs) != 1 {
		t.Fatalf("engine ran %d times, want 1: the retry must wait out the idle interval", len(fe.specs))
	}
}

func TestOnceReleaseNoteCarriesOutputTailWhenAgentLeftNoReport(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Priority: 1}}}
	fe := &fakeEngine{run: func(spec ContainerSpec) (int, error) {
		fmt.Fprint(spec.Output, "x", strings.Repeat("y", failureTailBytes), "\nRESULT: gave-up tests fail")
		return 0, nil
	}}
	r := newRunner(t, bare, fb, fe)
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	note := fb.calls[len(fb.calls)-1]
	if !strings.HasSuffix(note, "\nRESULT: gave-up tests fail") || strings.Contains(note, "xy") {
		t.Fatalf("release note = %q, want it to end with the output's tail", note)
	}
	if !strings.Contains(note, "left no failure report") || len(note) > failureTailBytes+200 {
		t.Fatalf("release note = %q (%d bytes), want the missing-report marker and a bounded tail", note, len(note))
	}
}

func TestOnceReleaseNoteOmitsTailWhenAgentCommented(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Priority: 1}}, comments: 4}
	fe := &fakeEngine{run: func(spec ContainerSpec) (int, error) {
		fmt.Fprint(spec.Output, "RESULT: gave-up see my comment")
		fb.comments++
		return 0, nil
	}}
	r := newRunner(t, bare, fb, fe)
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "release b-1 maker run ended without landing (exit 0) [attempt 1/3]"
	if last := fb.calls[len(fb.calls)-1]; last != want {
		t.Fatalf("last call = %q, want %q", last, want)
	}
}

func TestPromptRequiresFailureReportOnTheCard(t *testing.T) {
	p := Prompt(Bead{ID: "b-7", Title: "t"}, "")
	if !strings.Contains(p, "post a failure report on your card:\n   `sweatshop line cards comment b-7`") {
		t.Fatalf("prompt lacks the failure report step:\n%s", p)
	}
}

func TestAPICardsCommentCountReadsShownComments(t *testing.T) {
	for name, body := range map[string]string{
		"object": `{"id":"b-1","comments":[{"text":"a"},{"text":"b"}]}`,
		"array":  `[{"id":"b-1","comments":[{"text":"a"},{"text":"b"}]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
			defer ts.Close()
			n, err := (APICards{Client: station.Client{URL: ts.URL, Token: "mt"}}).CommentCount(context.Background(), "b-1")
			if err != nil || n != 2 {
				t.Fatalf("CommentCount = %d, %v; want 2, nil", n, err)
			}
		})
	}
}

func TestOnceReleasesClaimsStrandedByEarlierRun(t *testing.T) {
	bare, _ := remote(t)
	b := &fakeBeads{stranded: []Bead{{ID: "old-1"}}}
	r := newRunner(t, bare, b, &fakeEngine{})
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "release old-1 maker restarted; run did not settle"
	if !slices.Contains(b.calls, want) {
		t.Fatalf("calls = %q, want %q", b.calls, want)
	}
}

func TestOnceRetriesReleaseThenSucceeds(t *testing.T) {
	bare, _ := remote(t)
	b := &fakeBeads{ready: []Bead{{ID: "b-1", Priority: 1}}, failRelease: 2}
	r := newRunner(t, bare, b, &fakeEngine{})
	r.SettleRetryWait = time.Millisecond
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range b.calls {
		if strings.HasPrefix(c, "release b-1") {
			n++
		}
	}
	if n != 3 || slices.Contains(b.calls, "giveup b-1 needs:king") {
		t.Fatalf("calls = %q, want 3 releases and no escalation", b.calls)
	}
}

func TestOnceEscalatesToKingWhenCloseKeepsFailing(t *testing.T) {
	bare, work := remote(t)
	b := &fakeBeads{ready: []Bead{{ID: "b-1", Priority: 1}}, failClose: 99}
	e := &fakeEngine{run: func(ContainerSpec) (int, error) {
		commit(t, work, "work\n\nBead: b-1")
		return 0, nil
	}}
	r := newRunner(t, bare, b, e)
	r.SettleRetryWait = time.Millisecond
	if _, err := r.Once(context.Background()); err == nil {
		t.Fatal("Once = nil error, want the close failure")
	}
	if !slices.Contains(b.calls, "giveup b-1 needs:king") {
		t.Fatalf("calls = %q, want escalation to the king", b.calls)
	}
}

func TestOnceEscalatesToKingWhenReleaseKeepsFailing(t *testing.T) {
	bare, _ := remote(t)
	b := &fakeBeads{ready: []Bead{{ID: "b-1", Priority: 1}}, failRelease: 99}
	r := newRunner(t, bare, b, &fakeEngine{})
	r.SettleRetryWait = time.Millisecond
	if _, err := r.Once(context.Background()); err == nil {
		t.Fatal("Once = nil error, want the release failure")
	}
	if !slices.Contains(b.calls, "giveup b-1 needs:king") {
		t.Fatalf("calls = %q, want escalation to the king", b.calls)
	}
}

func TestOnceTellsStationsTheLinesStationURLAndExplicitURLWins(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1"}}}
	fe := &fakeEngine{}
	r := newRunner(t, bare, fb, fe)
	r.Line.URL = "http://sweatshop-a-serve:7469"
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fe.specs[0].Env["SWEATSHOP_LINE_URL"]; got != "http://sweatshop-a-serve:7469" {
		t.Fatalf("SWEATSHOP_LINE_URL = %q, want the line's url", got)
	}
	r.URL = "http://example.test:9"
	fb.ready = []Bead{{ID: "b-2"}}
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fe.specs[1].Env["SWEATSHOP_LINE_URL"]; got != "http://example.test:9" {
		t.Fatalf("SWEATSHOP_LINE_URL = %q, want the explicit url", got)
	}
}
