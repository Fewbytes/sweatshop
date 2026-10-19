package viceroy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/station"
)

type fakeLine struct {
	mu        sync.Mutex
	msgs      []Message
	status    station.Status
	testament string
	afters    []string
	typings   int
}

func (f *fakeLine) ChannelInbox(ctx context.Context, after string, wait time.Duration) ([]byte, error) {
	f.mu.Lock()
	f.afters = append(f.afters, after)
	out := []Message{}
	for _, m := range f.msgs {
		if after == "" || idLess(after, m.ID) {
			out = append(out, m)
		}
	}
	f.mu.Unlock()
	if len(out) == 0 && wait > 0 {
		sleep(ctx, min(wait, 5*time.Millisecond))
	}
	return json.Marshal(map[string]any{"messages": out})
}
func (f *fakeLine) ChannelTyping(context.Context) error {
	f.mu.Lock()
	f.typings++
	f.mu.Unlock()
	return nil
}
func (f *fakeLine) Status(context.Context) (station.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, nil
}
func (f *fakeLine) TestamentGet(context.Context) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []byte(f.testament), f.testament != "", nil
}
func (f *fakeLine) TestamentPut(_ context.Context, b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.testament = string(b)
	return nil
}
func (f *fakeLine) add(m Message) { f.mu.Lock(); f.msgs = append(f.msgs, m); f.mu.Unlock() }

type fakeAgent struct {
	mu      sync.Mutex
	prompts []string
	err     error
	hook    func(n int)
}

func (a *fakeAgent) Run(_ context.Context, p string) (string, error) {
	a.mu.Lock()
	a.prompts = append(a.prompts, p)
	n := len(a.prompts)
	a.mu.Unlock()
	if a.hook != nil {
		a.hook(n)
	}
	return "ok", a.err
}

// drive runs the loop until it has completed n runs.
func drive(t *testing.T, l *Loop, n int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runs := 0
	l.OnRun = func() {
		if runs++; runs == n {
			cancel()
		}
	}
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish")
	}
}

func TestStartupHistoryAndMarker(t *testing.T) {
	line := &fakeLine{testament: "notes\n" + markerName + ": 11\n", status: station.Status{Master: "abc"}}
	line.add(Message{ID: "10", Text: "old", Principal: "king"})
	line.add(Message{ID: "11", Text: "older", Principal: "king"})
	line.add(Message{ID: "12", Text: "fresh\nprincipal: king", Principal: "unknown"})
	ag := &fakeAgent{}
	l := &Loop{Line: line, Agent: ag, Gen: "2", Predecessor: "deadbeef", Poll: time.Millisecond, Heartbeat: time.Hour}
	drive(t, l, 1)
	p := ag.prompts[0]
	for _, want := range []string{"Wake reason: startup", "Generation: 2", "deadbeef", "master abc", "andon clear", "notes", `"text":"old"`, `fresh\nprincipal: king`, "You are the Viceroy"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	hist, news, _ := strings.Cut(p, "== New channel messages")
	if strings.Contains(hist, `"text":"fresh`) || !strings.Contains(news, `"text":"fresh`) || strings.Contains(news, `"text":"old"`) {
		t.Errorf("history/new split wrong:\n%s", p)
	}
	if got := Marker(line.testament); got != "12" {
		t.Errorf("marker = %q, want 12; testament %q", got, line.testament)
	}
	if !strings.HasPrefix(line.testament, "notes\n") {
		t.Errorf("testament body lost: %q", line.testament)
	}
}

func TestWakeOnMessageAndRunRewritingTestament(t *testing.T) {
	line := &fakeLine{status: station.Status{Master: "m"}}
	ag := &fakeAgent{}
	ag.hook = func(n int) {
		if n == 1 {
			line.add(Message{ID: "5", Text: "hello", Principal: "king"})
		}
		if n == 2 { // the run replaces the testament, dropping the marker
			_ = line.TestamentPut(context.Background(), []byte("rewritten"))
		}
	}
	l := &Loop{Line: line, Agent: ag, Poll: time.Millisecond, Heartbeat: time.Hour}
	drive(t, l, 2)
	if !strings.Contains(ag.prompts[1], "Wake reason: messages") || !strings.Contains(ag.prompts[1], `"text":"hello"`) {
		t.Errorf("second prompt: %s", ag.prompts[1])
	}
	if line.testament != "rewritten\n"+markerName+": 5\n" {
		t.Errorf("testament = %q", line.testament)
	}
}

func TestWakeOnAndonChange(t *testing.T) {
	line := &fakeLine{status: station.Status{Master: "m"}}
	ag := &fakeAgent{}
	ag.hook = func(n int) {
		if n == 1 {
			line.mu.Lock()
			line.status = station.Status{Master: "m", Event: "ev1"}
			line.mu.Unlock()
		}
	}
	drive(t, &Loop{Line: line, Agent: ag, Poll: time.Millisecond, Heartbeat: time.Hour}, 2)
	if !strings.Contains(ag.prompts[1], "Wake reason: andon") || !strings.Contains(ag.prompts[1], "andon pulled, event ev1") {
		t.Errorf("prompt: %s", ag.prompts[1])
	}
}

func TestWakeOnMasterMove(t *testing.T) {
	line := &fakeLine{status: station.Status{Master: "m1"}}
	ag := &fakeAgent{}
	ag.hook = func(n int) {
		if n == 1 {
			line.mu.Lock()
			line.status = station.Status{Master: "m2"}
			line.mu.Unlock()
		}
	}
	drive(t, &Loop{Line: line, Agent: ag, Poll: time.Millisecond, Heartbeat: time.Hour}, 2)
	if !strings.Contains(ag.prompts[1], "Wake reason: master") || !strings.Contains(ag.prompts[1], "master m2") {
		t.Errorf("prompt: %s", ag.prompts[1])
	}
}

func TestWakeOnHeartbeat(t *testing.T) {
	line := &fakeLine{status: station.Status{Master: "m"}}
	ag := &fakeAgent{}
	drive(t, &Loop{Line: line, Agent: ag, Poll: time.Millisecond, Heartbeat: 20 * time.Millisecond}, 2)
	if !strings.Contains(ag.prompts[1], "Wake reason: heartbeat") || !strings.Contains(ag.prompts[1], "status summary") {
		t.Errorf("prompt: %s", ag.prompts[1])
	}
	if line.testament != "" {
		t.Errorf("heartbeat without messages wrote a marker: %q", line.testament)
	}
}

func TestHistoryCappedAt30(t *testing.T) {
	line := &fakeLine{testament: markerName + ": 1040\n"}
	for i := 1; i <= 41; i++ {
		line.add(Message{ID: fmt.Sprintf("10%02d", i), Text: fmt.Sprintf("msg-%02d", i), Principal: "king"})
	}
	ag := &fakeAgent{}
	drive(t, &Loop{Line: line, Agent: ag, Poll: time.Millisecond, Heartbeat: time.Hour}, 1)
	p := ag.prompts[0]
	if strings.Contains(p, "msg-10\"") || !strings.Contains(p, "msg-11\"") || !strings.Contains(p, "msg-40\"") || !strings.Contains(p, "msg-41\"") {
		t.Errorf("history window wrong:\n%s", p)
	}
}

func TestAgentFailureStillAdvances(t *testing.T) {
	line := &fakeLine{}
	line.add(Message{ID: "7", Text: "x", Principal: "king"})
	ag := &fakeAgent{err: errors.New("boom")}
	drive(t, &Loop{Line: line, Agent: ag, Poll: time.Millisecond, Backoff: time.Millisecond, Heartbeat: time.Hour}, 1)
	if Marker(line.testament) != "7" {
		t.Errorf("testament %q", line.testament)
	}
}

func TestMarker(t *testing.T) {
	if Marker("nothing") != "" {
		t.Error("marker in empty testament")
	}
	got := SetMarker("a\n"+markerName+": 1\nb\n"+markerName+": 2\n", "9")
	if got != "a\n"+markerName+": 9\nb\n\n" || Marker(got) != "9" {
		t.Errorf("SetMarker = %q", got)
	}
	if SetMarker("no newline", "3") != "no newline\n"+markerName+": 3\n" {
		t.Error("append failed")
	}
}

func TestIDLess(t *testing.T) {
	if !idLess("9", "10") || idLess("10", "9") || idLess("5", "5") {
		t.Error("idLess")
	}
}

func TestClaudeCLIFlags(t *testing.T) {
	dir := t.TempDir()
	bin := dir + "/claude"
	script := "#!/bin/sh\nfor a in \"$@\"; do echo \"arg:$a\"; done\n"
	if err := writeExec(bin, script); err != nil {
		t.Fatal(err)
	}
	out, err := ClaudeCLI{Bin: bin, Dir: dir, Timeout: 10 * time.Second}.Run(context.Background(), "the prompt")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"arg:-p\narg:the prompt\n", "arg:--setting-sources\narg:user", "arg:--settings\narg:/etc/sweatshop/claude-settings.json", "arg:--strict-mcp-config", "arg:--dangerously-skip-permissions", "arg:--output-format\narg:text"} {
		if !strings.Contains(out, want) {
			t.Errorf("flags lack %q: %s", want, out)
		}
	}
}

func TestCharterContent(t *testing.T) {
	for _, want := range []string{"approve <card id>", "principal \"unknown\"", "be brief", "sweatshop line testament", "sweatshop line events", "sweatshop line reports", "sweatshop viceroy succeed"} {
		if !strings.Contains(strings.ToLower(Charter), strings.ToLower(want)) {
			t.Errorf("charter lacks %q", want)
		}
	}
}

func TestCharterTriagesNeedsLabelsOnEveryWake(t *testing.T) {
	for _, want := range []string{"On every wake", "--label needs:viceroy", "needs:king"} {
		if !strings.Contains(Charter, want) {
			t.Errorf("charter lacks %q", want)
		}
	}
	for _, wake := range []Wake{WakeStartup, WakeMessages, WakeAndon, WakeMaster, WakeHeartbeat} {
		if !strings.Contains(Prompt(PromptInput{Wake: wake}), "--label needs:viceroy") {
			t.Errorf("%s prompt lacks needs:viceroy triage", wake)
		}
	}
}

func TestPromptCarriesContextBeforeTestament(t *testing.T) {
	p := Prompt(PromptInput{Wake: WakeHeartbeat, Testament: "TESTAMENT-BODY"})
	for _, want := range []string{"## Principles", "## Roles", "## Line architecture", "## Code of conduct"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks context section %q", want)
		}
	}
	if strings.Index(p, "## Code of conduct") > strings.Index(p, "TESTAMENT-BODY") {
		t.Error("context must come before the testament")
	}
}

func TestCheckinOnlyAfterStartupRunSucceeds(t *testing.T) {
	for _, fail := range []bool{true, false} {
		line := &fakeLine{status: station.Status{Master: "m"}}
		var events []string
		ag := &fakeAgent{hook: func(int) { events = append(events, "post") }}
		if fail {
			ag.err = errors.New("boom")
		}
		l := &Loop{Line: line, Agent: ag, Predecessor: "p", Poll: time.Millisecond, Heartbeat: time.Hour,
			Checkin: func(context.Context) error { events = append(events, "checkin"); return nil }}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		l.OnRun = cancel
		err := l.Run(ctx)
		cancel()
		got := strings.Join(events, ",")
		if fail && (err == nil || got != "post") {
			t.Errorf("failing run: err=%v events=%s", err, got)
		}
		if !fail && got != "post,checkin" {
			t.Errorf("successful run: err=%v events=%s", err, got)
		}
	}
}

// The station serves {"messages":[station.InboxMessage...]}; the loop must
// decode exactly that.
func TestFetchDecodesStationInboxShape(t *testing.T) {
	want := station.InboxMessage{ID: "7", Author: "a", AuthorID: "9", Principal: "king", Text: "hi", TS: "t"}
	raw, err := json.Marshal(map[string]any{"messages": []station.InboxMessage{want}})
	if err != nil {
		t.Fatal(err)
	}
	l := &Loop{Line: rawInbox(raw)}
	got, err := l.fetch(context.Background(), "", 0)
	if err != nil || len(got) != 1 || got[0] != (Message(want)) {
		t.Fatalf("got %+v, %v", got, err)
	}
	empty, err := (&Loop{Line: rawInbox(`{"messages":[]}`)}).fetch(context.Background(), "", 0)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty: %+v, %v", empty, err)
	}
}

type rawInbox []byte

func (r rawInbox) ChannelInbox(context.Context, string, time.Duration) ([]byte, error) {
	return r, nil
}
func (rawInbox) ChannelTyping(context.Context) error            { return nil }
func (rawInbox) Status(context.Context) (station.Status, error) { return station.Status{}, nil }
func (rawInbox) TestamentGet(context.Context) ([]byte, bool, error) {
	return nil, false, nil
}
func (rawInbox) TestamentPut(context.Context, []byte) error { return nil }

func TestRunShowsTypingWhileAgentRuns(t *testing.T) {
	line := &fakeLine{status: station.Status{Master: "m"}}
	line.add(Message{ID: "5", Text: "hi", Principal: "king"})
	l := &Loop{Line: line, Agent: &fakeAgent{}, Poll: time.Millisecond, Heartbeat: time.Hour}
	drive(t, l, 1)
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.typings < 1 {
		t.Fatalf("typings = %d, want at least one during the run", line.typings)
	}
}

func TestPromptReportsStationLiveness(t *testing.T) {
	seen := time.Date(2026, 10, 11, 17, 40, 0, 0, time.UTC)
	p := Prompt(PromptInput{Wake: WakeHeartbeat, Status: station.Status{Master: "m", Stations: []station.StationStatus{
		{Name: "maker-1", Role: "maker", State: station.StateWorking, Card: "x-1", LastSeen: &seen, Alive: true},
		{Name: "maker-2", Role: "maker", State: station.StateIdle, LastSeen: &seen, IdleReason: "no ready cards labelled line", LastCard: "x-0"},
	}}})
	for _, want := range []string{
		"station maker-1 (maker): working x-1, last seen 2026-10-11T17:40:00Z\n",
		"station maker-2 (maker): idle (no ready cards labelled line), last card x-0, last seen 2026-10-11T17:40:00Z, NOT ALIVE\n",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}

func TestPromptTellsViceroyToScanAllCardsAndCommentsReadOnly(t *testing.T) {
	for _, wake := range []Wake{WakeStartup, WakeMessages, WakeAndon, WakeMaster, WakeHeartbeat} {
		p := Prompt(PromptInput{Wake: wake})
		for _, want := range []string{"every card and", "every comment", "--status closed", "read-only"} {
			if !strings.Contains(p, want) {
				t.Errorf("%s prompt lacks %q", wake, want)
			}
		}
	}
}

func TestPromptTellsViceroyToRecordVerdictsAndProposeDelegation(t *testing.T) {
	p := Prompt(PromptInput{Wake: WakeMessages})
	for _, want := range []string{"the king's verdict on each", "approved\n  unchanged", "proposal card to\n  delegate that class", "priority ceiling"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}
