package andon

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/station"
	"github.com/Fewbytes/sweatshop/line/witness"
)

type fakeAPI struct {
	status    station.Status
	passed    map[string]bool
	attestErr error
	pullErr   error
	clearErr  error
	pullRes   guard.Result
	clearRes  guard.Result
	attests   []string
	pulls     int
	clears    int
}

func (f *fakeAPI) Status(context.Context) (station.Status, error) { return f.status, nil }
func (f *fakeAPI) AttestLine(_ context.Context, c string) (witness.AttestResult, error) {
	f.attests = append(f.attests, c)
	if f.attestErr != nil {
		return witness.AttestResult{}, f.attestErr
	}
	return witness.AttestResult{Commit: c, Passed: f.passed[c], Envelope: &attest.Envelope{}}, nil
}
func (f *fakeAPI) Pull(context.Context, attest.Envelope) (guard.Result, error) {
	f.pulls++
	return f.pullRes, f.pullErr
}
func (f *fakeAPI) Clear(context.Context, attest.Envelope) (guard.Result, error) {
	f.clears++
	return f.clearRes, f.clearErr
}

// fakeCards behaves like BeadCards: Raise returns the live (open or
// in_progress) card for an event, and files a new one only when none is live.
// events records every card filed.
type fakeCards struct {
	events   []string
	masters  []string        // master named on each filed card
	live     map[string]bool // event -> has a live card
	failures int             // Raise calls that fail before it starts succeeding
	calls    int

	resolved   []string // events passed to Resolve
	resolveErr error
}

func (c *fakeCards) Raise(_ context.Context, event string, _ guard.AndonEvent, master string) (string, error) {
	c.calls++
	if c.failures > 0 {
		c.failures--
		return "", errors.New("bd down")
	}
	if c.live[event] {
		return "card-live", nil
	}
	if c.live == nil {
		c.live = map[string]bool{}
	}
	c.live[event] = true
	c.events = append(c.events, event)
	c.masters = append(c.masters, master)
	return "card-new", nil
}

func (c *fakeCards) Resolve(_ context.Context, event, _ string) error {
	c.resolved = append(c.resolved, event)
	delete(c.live, event)
	return c.resolveErr
}

// closeAll closes every card, as Landed does when a fix lands.
func (c *fakeCards) closeAll() { c.live = nil }

func newWatcher(api *fakeAPI) (*Watcher, *fakeCards) {
	cards := &fakeCards{}
	return &Watcher{API: api, Cards: cards, Log: log.New(io.Discard, "", 0)}, cards
}

func step(t *testing.T, w *Watcher) {
	t.Helper()
	if err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestGreenMasterNoPull(t *testing.T) {
	api := &fakeAPI{status: station.Status{Master: "aaa"}, passed: map[string]bool{"aaa": true}}
	w, cards := newWatcher(api)
	step(t, w)
	if api.pulls != 0 || api.clears != 0 || len(cards.events) != 0 {
		t.Fatalf("pulls=%d clears=%d cards=%v", api.pulls, api.clears, cards.events)
	}
}

func TestFailingMasterPullsOnceAndRaisesCard(t *testing.T) {
	api := &fakeAPI{
		status:  station.Status{Master: "bbb"},
		passed:  map[string]bool{},
		pullRes: guard.Result{Status: guard.Stopped, Event: "ev1"},
	}
	w, cards := newWatcher(api)
	step(t, w)
	step(t, w)
	if api.pulls != 1 || len(api.attests) != 1 || len(cards.events) != 1 || cards.events[0] != "ev1" {
		t.Fatalf("pulls=%d attests=%v cards=%v", api.pulls, api.attests, cards.events)
	}
}

func TestNewPassingMasterClears(t *testing.T) {
	api := &fakeAPI{
		status:   station.Status{Master: "bbb"},
		passed:   map[string]bool{"ccc": true},
		pullRes:  guard.Result{Status: guard.Stopped, Event: "ev1"},
		clearRes: guard.Result{Status: guard.Cleared},
	}
	w, _ := newWatcher(api)
	step(t, w)
	api.status = station.Status{Master: "ccc", Event: "ev1"}
	step(t, w)
	if api.clears != 1 {
		t.Fatalf("clears=%d", api.clears)
	}
	step(t, w)
	if api.clears != 1 || len(api.attests) != 2 {
		t.Fatalf("repeat work: clears=%d attests=%v", api.clears, api.attests)
	}
}

func TestAttestErrorRetries(t *testing.T) {
	api := &fakeAPI{status: station.Status{Master: "aaa"}, passed: map[string]bool{"aaa": true}, attestErr: errors.New("boom")}
	w, _ := newWatcher(api)
	if err := w.Step(context.Background()); err == nil {
		t.Fatal("want error")
	}
	api.attestErr = nil
	step(t, w)
	if len(api.attests) != 2 || w.last != "aaa" {
		t.Fatalf("attests=%v last=%q", api.attests, w.last)
	}
}

func TestStaleEvidenceRetries(t *testing.T) {
	api := &fakeAPI{
		status:  station.Status{Master: "bbb"},
		passed:  map[string]bool{},
		pullRes: guard.Result{Status: guard.Rejected, Reason: guard.ReasonStaleEvidence},
	}
	w, cards := newWatcher(api)
	step(t, w)
	step(t, w)
	if api.pulls != 2 || len(cards.events) != 0 {
		t.Fatalf("pulls=%d cards=%v", api.pulls, cards.events)
	}
}

func TestRaiseFailureRetriedNextStep(t *testing.T) {
	api := &fakeAPI{
		status:  station.Status{Master: "bbb"},
		passed:  map[string]bool{},
		pullRes: guard.Result{Status: guard.Stopped, Event: "ev1"},
	}
	w, cards := newWatcher(api)
	cards.failures = 1
	if err := w.Step(context.Background()); err == nil {
		t.Fatal("want raise error")
	}
	api.status = station.Status{Master: "bbb", Event: "ev1"}
	step(t, w)
	step(t, w)
	if len(cards.events) != 1 || cards.events[0] != "ev1" || api.pulls != 1 {
		t.Fatalf("cards=%v pulls=%d", cards.events, api.pulls)
	}
}

func TestRestartCardsOpenEvent(t *testing.T) {
	api := &fakeAPI{
		status:  station.Status{Master: "bbb", Event: "ev0", Andon: &guard.AndonEvent{Commit: "bbb", Check: "test"}},
		passed:  map[string]bool{},
		pullRes: guard.Result{Status: guard.Rejected, Reason: guard.ReasonAlreadyStopped, Event: "ev0"},
	}
	w, cards := newWatcher(api)
	step(t, w)
	if len(cards.events) != 1 || cards.events[0] != "ev0" {
		t.Fatalf("cards=%v", cards.events)
	}
}

func TestPullErrorRetries(t *testing.T) {
	api := &fakeAPI{status: station.Status{Master: "bbb"}, passed: map[string]bool{}, pullErr: errors.New("net")}
	w, _ := newWatcher(api)
	if err := w.Step(context.Background()); err == nil {
		t.Fatal("want error")
	}
	api.pullErr = nil
	api.pullRes = guard.Result{Status: guard.Stopped, Event: "ev1"}
	step(t, w)
	if api.pulls != 2 {
		t.Fatalf("pulls=%d", api.pulls)
	}
}

func TestClearErrorRetries(t *testing.T) {
	api := &fakeAPI{
		status:   station.Status{Master: "ccc", Event: "ev1"},
		passed:   map[string]bool{"ccc": true},
		clearErr: errors.New("net"),
		clearRes: guard.Result{Status: guard.Cleared},
	}
	w, _ := newWatcher(api)
	if err := w.Step(context.Background()); err == nil {
		t.Fatal("want error")
	}
	api.clearErr = nil
	step(t, w)
	if api.clears != 2 {
		t.Fatalf("clears=%d", api.clears)
	}
}

func TestAlreadyStoppedCardedOnce(t *testing.T) {
	api := &fakeAPI{
		status:  station.Status{Master: "bbb", Event: "ev0"},
		passed:  map[string]bool{},
		pullRes: guard.Result{Status: guard.Rejected, Reason: guard.ReasonAlreadyStopped, Event: "ev0"},
	}
	w, cards := newWatcher(api)
	step(t, w)
	step(t, w)
	if api.pulls != 1 || len(cards.events) != 1 {
		t.Fatalf("pulls=%d cards=%v", api.pulls, cards.events)
	}
}

// I2: the fix lands (its card closes) but the new master still fails. The line
// stays stopped, so it must get a fresh card naming the still-failing master.
func TestFixLandsMasterStillFailsCardClosedFilesNewCard(t *testing.T) {
	api := &fakeAPI{
		status:  station.Status{Master: "c1"},
		passed:  map[string]bool{},
		pullRes: guard.Result{Status: guard.Stopped, Event: "ev1"},
	}
	w, cards := newWatcher(api)
	step(t, w)
	if len(cards.events) != 1 {
		t.Fatalf("cards=%v", cards.events)
	}
	cards.closeAll()
	api.status = station.Status{Master: "c2", Event: "ev1", Andon: &guard.AndonEvent{Commit: "c1", Check: "ci"}}
	api.pullRes = guard.Result{Status: guard.Rejected, Reason: guard.ReasonAlreadyStopped, Event: "ev1"}
	step(t, w)
	if len(cards.events) != 2 || cards.events[1] != "ev1" || cards.masters[1] != "c2" {
		t.Fatalf("no fresh card after the fix failed to clear: events=%v masters=%v", cards.events, cards.masters)
	}
	// Still stopped, card live: further steps raise idempotently, file nothing.
	step(t, w)
	step(t, w)
	if len(cards.events) != 2 || len(api.attests) != 2 {
		t.Fatalf("events=%v attests=%v", cards.events, api.attests)
	}
}

// A card closed by a fix that does clear the line must not be re-filed in
// the window before the watcher attests the fix.
func TestFixThatClearsFilesNoNewCard(t *testing.T) {
	api := &fakeAPI{
		status:   station.Status{Master: "c1"},
		passed:   map[string]bool{"c2": true},
		pullRes:  guard.Result{Status: guard.Stopped, Event: "ev1"},
		clearRes: guard.Result{Status: guard.Cleared},
	}
	w, cards := newWatcher(api)
	step(t, w)
	cards.closeAll()
	api.status = station.Status{Master: "c2", Event: "ev1", Andon: &guard.AndonEvent{Commit: "c1"}}
	step(t, w)
	if api.clears != 1 || len(cards.events) != 1 {
		t.Fatalf("clears=%d cards=%v", api.clears, cards.events)
	}
}

// While the line stays stopped on an already-attested master, every step
// re-checks the card (not once per process).
func TestStoppedLineRaisesEveryStep(t *testing.T) {
	api := &fakeAPI{
		status:  station.Status{Master: "bbb", Event: "ev0"},
		passed:  map[string]bool{},
		pullRes: guard.Result{Status: guard.Rejected, Reason: guard.ReasonAlreadyStopped, Event: "ev0"},
	}
	w, cards := newWatcher(api)
	step(t, w)
	cards.closeAll() // e.g. an operator closed the card by mistake
	step(t, w)
	step(t, w)
	if cards.calls != 3 || len(cards.events) != 2 {
		t.Fatalf("raise calls=%d cards=%v", cards.calls, cards.events)
	}
}

func TestBeadCardsListsOpenAndInProgress(t *testing.T) {
	dir := t.TempDir()
	listArgv := filepath.Join(dir, "list-argv")
	script := "#!/bin/sh\nif [ \"$1\" = list ]; then printf '%s\\n' \"$@\" > " + listArgv +
		"; echo '[{\"id\":\"sweatshop-held\",\"status\":\"in_progress\",\"description\":\"event: ev7\"}]'; exit 0; fi\necho create >&2; exit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	id, err := BeadCards{Dir: t.TempDir()}.Raise(context.Background(), "ev7", guard.AndonEvent{Commit: "abc"}, "abc")
	if err != nil || id != "sweatshop-held" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	raw, err := os.ReadFile(listArgv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "--status\nopen,in_progress\n") {
		t.Fatalf("bd list argv = %q", raw)
	}
}

func TestBeadCardsNamesStillFailingMaster(t *testing.T) {
	argv := fakeBD(t, `[]`)
	if _, err := (BeadCards{Dir: t.TempDir()}).Raise(context.Background(), "ev1", guard.AndonEvent{Commit: "c1", Check: "ci"}, "c2"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\nmaster: c2\n", "A fix landed but master still fails", "andon: master c2 fails ci"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("card lacks %q: %q", want, raw)
		}
	}
}

// fakeBD puts a `bd` first on PATH. `bd list` prints listJSON; `bd create`
// records its argv to the returned file and prints a new ID.
func fakeBD(t *testing.T, listJSON string) (createArgv string) {
	t.Helper()
	dir := t.TempDir()
	createArgv = filepath.Join(dir, "argv")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = list ]; then cat <<'EOF'\n" + listJSON + "\nEOF\nexit 0; fi\n" +
		"printf '%s\\n' \"$@\" > " + createArgv + "\necho '{\"id\":\"sweatshop-x1\"}'\n"
	if err := os.WriteFile(filepath.Join(dir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return createArgv
}

func TestBeadCardsIdempotent(t *testing.T) {
	argv := fakeBD(t, `[{"id":"sweatshop-old","description":"x\n\nevent: ev123\ncommit: abc"}]`)
	id, err := BeadCards{Dir: t.TempDir()}.Raise(context.Background(), "ev123", guard.AndonEvent{Commit: "abc"}, "abc")
	if err != nil || id != "sweatshop-old" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if _, err := os.Stat(argv); err == nil {
		t.Fatal("bd create was called for an event that already has a card")
	}
}

func TestBeadCardsOtherEventStillCreates(t *testing.T) {
	argv := fakeBD(t, `[{"id":"sweatshop-old","description":"event: ev999"}]`)
	id, err := BeadCards{Dir: t.TempDir()}.Raise(context.Background(), "ev123", guard.AndonEvent{Commit: "abc"}, "abc")
	if err != nil || id != "sweatshop-x1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	raw, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "fails unknown check") {
		t.Fatalf("title lacks fallback check name: %q", raw)
	}
}

func TestBeadCards(t *testing.T) {
	argv := fakeBD(t, `[]`)

	id, err := BeadCards{Dir: t.TempDir()}.Raise(context.Background(), "ev123", guard.AndonEvent{
		Commit: "0123456789abcdef0123456789abcdef01234567", Check: "test", ExitCode: 2, EvidenceID: "evid9",
	}, "0123456789abcdef0123456789abcdef01234567")
	if err != nil || id != "sweatshop-x1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	raw, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	has := func(a, b string) bool {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == a && args[i+1] == b {
				return true
			}
		}
		return false
	}
	if args[0] != "create" || args[1] != "andon: master 0123456789ab fails test" ||
		!has("-p", "0") || !has("-l", "line") || !has("-l", "andon") || !has("-t", "bug") {
		t.Fatalf("argv = %q", args)
	}
	desc := strings.Join(args, "\n")
	for _, want := range []string{"\nevent: ev123\n", "0123456789abcdef0123456789abcdef01234567", "evid9", "Andon-Event: ev123"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description lacks %q", want)
		}
	}
}

func TestAttestErrorOnStoppedLineStillCards(t *testing.T) {
	api := &fakeAPI{
		status:    station.Status{Master: "bbb", Event: "ev0", Andon: &guard.AndonEvent{Commit: "aaa"}},
		attestErr: errors.New("boom"),
	}
	w, cards := newWatcher(api)
	if err := w.Step(context.Background()); err == nil {
		t.Fatal("want error")
	}
	cards.closeAll()
	if err := w.Step(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if cards.calls != 2 || len(cards.events) != 2 || cards.masters[0] != "bbb" {
		t.Fatalf("raise calls=%d cards=%v masters=%v", cards.calls, cards.events, cards.masters)
	}
}

func TestClearResolvesCards(t *testing.T) {
	api := &fakeAPI{
		status:   station.Status{Master: "bbb"},
		passed:   map[string]bool{"ccc": true},
		pullRes:  guard.Result{Status: guard.Stopped, Event: "ev1"},
		clearRes: guard.Result{Status: guard.Cleared},
	}
	w, cards := newWatcher(api)
	step(t, w)
	api.status = station.Status{Master: "ccc", Event: "ev1"}
	step(t, w)
	if len(cards.resolved) != 1 || cards.resolved[0] != "ev1" || cards.live["ev1"] {
		t.Fatalf("resolved=%v live=%v", cards.resolved, cards.live)
	}
}

func TestResolveFailureDoesNotFailStep(t *testing.T) {
	api := &fakeAPI{
		status:   station.Status{Master: "ccc", Event: "ev1"},
		passed:   map[string]bool{"ccc": true},
		clearRes: guard.Result{Status: guard.Cleared},
	}
	w, cards := newWatcher(api)
	cards.resolveErr = errors.New("bd down")
	step(t, w)
	if api.clears != 1 || len(cards.resolved) != 1 {
		t.Fatalf("clears=%d resolved=%v", api.clears, cards.resolved)
	}
}

func TestBeadCardsResolveClosesLiveCards(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "closed")
	script := "#!/bin/sh\nif [ \"$1\" = list ]; then cat <<'EOF'\n" +
		`[{"id":"c1","description":"event: ev1"},{"id":"c2","description":"event: ev2"},{"id":"c3","description":"x\nevent: ev1"}]` +
		"\nEOF\nexit 0; fi\nprintf '%s\\n' \"$@\" >> " + log + "\n"
	if err := os.WriteFile(filepath.Join(dir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := (BeadCards{Dir: t.TempDir()}).Resolve(context.Background(), "ev1", "mmm"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(log)
	got := string(raw)
	if !strings.Contains(got, "close\nc1\n") || !strings.Contains(got, "close\nc3\n") || strings.Contains(got, "c2") {
		t.Fatalf("bd close calls = %q", got)
	}
}
