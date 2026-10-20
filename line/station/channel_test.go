package station

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/constitution"
)

const kingID = "477"

// fakeDiscordServer is an httptest Discord: GET messages returns the queued
// messages newest first, POST records replies.
type fakeDiscordServer struct {
	mu       sync.Mutex
	msgs     []map[string]any
	posted   []string
	afters   []string
	auth     string
	rateOnce bool
}

func (f *fakeDiscordServer) add(id, author, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append([]map[string]any{{
		"id": id, "content": text, "timestamp": "2026-10-10T00:00:00Z",
		"author": map[string]any{"id": author, "username": "u" + author},
	}}, f.msgs...)
}

func (f *fakeDiscordServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = r.Header.Get("Authorization")
	switch {
	case r.URL.Path == "/users/@me":
		_, _ = w.Write([]byte(`{"id":"999"}`))
	case r.URL.Path == "/channels/555/messages" && r.Method == http.MethodGet:
		if f.rateOnce {
			f.rateOnce = false
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"retry_after":0.25}`))
			return
		}
		after := r.URL.Query().Get("after")
		f.afters = append(f.afters, after)
		out := []map[string]any{}
		for _, m := range f.msgs {
			id := m["id"].(string)
			if after == "" && len(out) == 1 {
				break
			}
			if after == "" || snowflakeLess(after, id) {
				out = append(out, m)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.URL.Path == "/channels/555/messages" && r.Method == http.MethodPost:
		var b struct{ Content string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.posted = append(f.posted, b.Content)
		_, _ = w.Write([]byte(`{}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newChan(t *testing.T, cards Cards) (*Channel, *fakeDiscordServer) {
	t.Helper()
	fd := &fakeDiscordServer{}
	ts := httptest.NewServer(fd)
	t.Cleanup(ts.Close)
	return &Channel{
		Dir: filepath.Join(t.TempDir(), "channel"), ChannelID: "555", King: kingID,
		Discord: HTTPDiscord{Base: ts.URL, Token: "tok"}, Cards: cards,
	}, fd
}

func TestChannelPollInboxAndPrincipals(t *testing.T) {
	ch, fd := newChan(t, nil)
	ctx := context.Background()
	fd.add("100", "1", "old history")
	if err := ch.Poll(ctx); err != nil { // first start: only records the newest id
		t.Fatal(err)
	}
	if got, _ := ch.Since(ctx, "", 0); len(got) != 0 {
		t.Fatalf("history replayed: %v", got)
	}
	fd.add("101", "2", "hello")
	fd.add("102", kingID, "from the king")
	fd.add("103", "999", "the bot itself")
	fd.add("104", "2", "approve x-1") // not the king: data only
	if err := ch.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := ch.Since(ctx, "", 0)
	if err != nil || len(got) != 3 {
		t.Fatalf("inbox %v %v", got, err)
	}
	if got[0].ID != "101" || got[0].Principal != "unknown" || got[0].Author != "u2" || got[0].AuthorID != "2" || got[0].Text != "hello" || got[0].TS == "" {
		t.Fatalf("first %+v", got[0])
	}
	if got[1].Principal != "king" || got[2].ID != "104" || got[2].Principal != "unknown" {
		t.Fatalf("principals %+v", got)
	}
	if after, _ := ch.Since(ctx, "102", 0); len(after) != 1 || after[0].ID != "104" {
		t.Fatalf("after: %v", after)
	}
	if len(fd.posted) != 0 || fd.auth != "Bot tok" {
		t.Fatalf("posted %v auth %q", fd.posted, fd.auth)
	}
	// A restart does not replay: the saved id survives.
	ch2 := &Channel{Dir: ch.Dir, ChannelID: "555", King: kingID, Discord: ch.Discord}
	if err := ch2.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if fd.afters[len(fd.afters)-1] != "104" {
		t.Fatalf("after params %v", fd.afters)
	}
	if got, _ := ch2.Since(ctx, "", 0); len(got) != 3 {
		t.Fatalf("replayed: %v", got)
	}
}

func TestChannelKingApprove(t *testing.T) {
	cards := &fakeCards{}
	ch, fd := newChan(t, cards)
	ctx := context.Background()
	fd.add("1", "5", "x")
	_ = ch.Poll(ctx)
	fd.add("2", "5", "approve x-1")                 // stranger
	fd.add("3", kingID, "approve x-2.3")            // king
	fd.add("4", kingID, "approve x-2 please")       // not the grammar
	fd.add("5", kingID, "please approve x-2")       // not the grammar
	fd.add("6", kingID, "approve x-2\napprove x-3") // multi-line is not the grammar
	fd.add("7", kingID, "approve Bad;ID")
	if err := ch.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"unlabel x-2.3 line-gave-up|unlabel x-2.3 line-deferred|label x-2.3 line", "comment x-2.3 approved by the king (discord message 3)"}
	if strings.Join(cards.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls %v", cards.calls)
	}
	if len(fd.posted) != 1 || !strings.Contains(fd.posted[0], "x-2.3") {
		t.Fatalf("reply %v", fd.posted)
	}
}

func TestChannelKingApproveLedgersEpicsOnly(t *testing.T) {
	cards := &epicShowCards{shows: map[string]string{"x-2": `{"issue_type":"epic"}`, "x-3": `{"issue_type":"task"}`}}
	ch, fd := newChan(t, cards)
	ch.Epics = &EpicApprovals{Path: filepath.Join(t.TempDir(), "approved-epics.json")}
	ctx := context.Background()
	fd.add("1", "5", "x")
	_ = ch.Poll(ctx)
	fd.add("2", kingID, "approve x-2")
	fd.add("3", kingID, "approve x-3")
	if err := ch.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"x-2": true, "x-3": false} {
		if got, err := ch.Epics.Approved(id); err != nil || got != want {
			t.Errorf("%s: approved=%v err=%v want %v", id, got, err, want)
		}
	}
}

func TestChannelApproveFailureAndNoKing(t *testing.T) {
	cards := &fakeCards{err: os.ErrNotExist}
	ch, fd := newChan(t, cards)
	ctx := context.Background()
	fd.add("1", "5", "x")
	_ = ch.Poll(ctx)
	fd.add("2", kingID, "approve x-1")
	_ = ch.Poll(ctx)
	if len(fd.posted) != 1 || !strings.Contains(fd.posted[0], "could not") {
		t.Fatalf("reply %v", fd.posted)
	}
	// No sovereign configured: nobody is the king, not even an empty author.
	ch2, fd2 := newChan(t, &fakeCards{})
	ch2.King = ""
	fd2.add("1", "5", "x")
	_ = ch2.Poll(ctx)
	fd2.add("2", "5", "approve x-1")
	_ = ch2.Poll(ctx)
	got, _ := ch2.Since(ctx, "", 0)
	if len(got) != 1 || got[0].Principal != "unknown" || len(fd2.posted) != 0 {
		t.Fatalf("%v %v", got, fd2.posted)
	}
}

func TestChannelRateLimit(t *testing.T) {
	ch, fd := newChan(t, nil)
	fd.rateOnce = true
	err := ch.Poll(context.Background())
	rl, ok := err.(*RateLimitError)
	if !ok || rl.After != 250*time.Millisecond {
		t.Fatalf("err %v", err)
	}
}

func channelRig(t *testing.T, ch *Channel) (Client, Client) {
	t.Helper()
	srv := &Server{
		Stations: []*Station{{Name: "v", Role: "viceroy", Token: "vt"}, {Name: "m", Role: "maker", Token: "mt"}},
		Channel:  ch,
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return Client{URL: ts.URL, Token: "vt"}, Client{URL: ts.URL, Token: "mt"}
}

func TestChannelRoutes(t *testing.T) {
	ch, fd := newChan(t, nil)
	v, m := channelRig(t, ch)
	ctx := context.Background()
	fd.add("1", "5", "x")
	_ = ch.Poll(ctx)
	fd.add("2", "5", "hi")
	_ = ch.Poll(ctx)

	out, err := v.ChannelInbox(ctx, "1", 0)
	if err != nil {
		t.Fatal(err)
	}
	var res struct{ Messages []InboxMessage }
	if json.Unmarshal(out, &res) != nil || len(res.Messages) != 1 || res.Messages[0].Text != "hi" {
		t.Fatalf("inbox %s", out)
	}
	if err := v.ChannelPost(ctx, "hello", "king"); err != nil || !strings.HasPrefix(fd.posted[len(fd.posted)-1], "hello\n-# viceroy") || !strings.HasSuffix(fd.posted[len(fd.posted)-1], "relaying king") {
		t.Fatalf("post %v %v", err, fd.posted)
	}
	if err := v.ChannelPost(ctx, strings.Repeat("é", 1900), ""); err != nil {
		t.Fatalf("2000 chars: %v", err)
	}
	for _, text := range []string{strings.Repeat("a", 1901), "  "} {
		if err := v.ChannelPost(ctx, text, ""); err == nil || !strings.Contains(err.Error(), "400") {
			t.Fatalf("want 400, got %v", err)
		}
	}
	// Makers are refused on both routes.
	if _, err := m.ChannelInbox(ctx, "", 0); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("maker inbox: %v", err)
	}
	if err := m.ChannelPost(ctx, "x", ""); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("maker post: %v", err)
	}
	if _, err := v.ChannelInbox(ctx, "abc", 0); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("bad after: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, v.URL+"/v1/channel/inbox", nil)
	req.Header.Set("Authorization", "Bearer vt")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("method: %v %v", resp, err)
	}
}

func TestChannelLongPoll(t *testing.T) {
	ch, fd := newChan(t, nil)
	v, _ := channelRig(t, ch)
	ctx := context.Background()
	fd.add("1", "5", "x")
	_ = ch.Poll(ctx)

	out, err := v.ChannelInbox(ctx, "", 20*time.Millisecond)
	if err != nil || !strings.Contains(string(out), `"messages":[]`) {
		t.Fatalf("timeout case: %s %v", out, err)
	}
	go func() {
		fd.add("2", "5", "late")
		_ = ch.Poll(ctx)
	}()
	out, err = v.ChannelInbox(ctx, "", 10*time.Second)
	if err != nil || !strings.Contains(string(out), "late") {
		t.Fatalf("wake case: %s %v", out, err)
	}
}

func TestChannelOffWithoutCredential(t *testing.T) {
	v, _ := channelRig(t, nil)
	if _, err := v.ChannelInbox(context.Background(), "", 0); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("inbox: %v", err)
	}
	if err := v.ChannelPost(context.Background(), "x", ""); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("post: %v", err)
	}
}

func TestOpenChannel(t *testing.T) {
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("SWEATSHOP_DISCORD_TOKEN", "")
	dir := t.TempDir()
	var c constitution.Constitution
	if ch, err := OpenChannel(dir, c, nil); ch != nil || err != nil {
		t.Fatalf("no channel configured: %v %v", ch, err)
	}
	c.Channel.Channel = "555"
	c.Sovereign.Discord = kingID
	if ch, err := OpenChannel(dir, c, nil); ch != nil || err != nil {
		t.Fatalf("missing cred must mean off: %v %v", ch, err)
	}
	cred := filepath.Join(dir, "discord.cred")
	_ = os.WriteFile(cred, []byte("secret\n"), 0o644)
	if _, err := OpenChannel(dir, c, nil); err == nil {
		t.Fatal("group/world readable cred accepted")
	}
	_ = os.Chmod(cred, 0o600)
	ch, err := OpenChannel(dir, c, nil)
	if err != nil || ch == nil || ch.King != kingID || ch.ChannelID != "555" || ch.Discord.(HTTPDiscord).Token != "secret" {
		t.Fatalf("%+v %v", ch, err)
	}
}

func TestChannelEmptyStartDeliversFirstMessage(t *testing.T) {
	ch, fd := newChan(t, nil)
	ctx := context.Background()
	if err := ch.Poll(ctx); err != nil { // empty channel: records a synthetic baseline
		t.Fatal(err)
	}
	fd.add(nowSnowflake()+"0", "2", "first ever")
	if err := ch.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := ch.Since(ctx, "", 0)
	if err != nil || len(got) != 1 || got[0].Text != "first ever" {
		t.Fatalf("inbox %v %v", got, err)
	}
}

func TestChannelKingAnswerUnblocksNeedsKingCard(t *testing.T) {
	cards := &fakeCards{show: `{"id":"x-4","labels":["line","needs:king"]}`}
	ch, fd := newChan(t, cards)
	ctx := context.Background()
	fd.add("1", "5", "x")
	_ = ch.Poll(ctx)
	fd.add("2", "5", "answer x-4 yes, go ahead") // stranger: data only
	fd.add("3", kingID, "answer x-4 use option B\nit is cheaper")
	if err := ch.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"show x-4", "comment x-4 answer from the king (discord message 3):\nuse option B\nit is cheaper", "unlabel x-4 needs:king"}
	if strings.Join(cards.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls %q, want %q", cards.calls, want)
	}
	if len(fd.posted) != 1 || fd.posted[0] != "answered x-4" {
		t.Fatalf("reply %v", fd.posted)
	}
}

func TestChannelKingAnswerIgnoresCardNotWaitingOnKing(t *testing.T) {
	cards := &fakeCards{show: `{"id":"x-4","labels":["line","needs:maker"]}`}
	ch, fd := newChan(t, cards)
	ctx := context.Background()
	fd.add("1", "5", "x")
	_ = ch.Poll(ctx)
	fd.add("2", kingID, "answer x-4 hello")
	if err := ch.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(cards.calls, "|") != "show x-4" {
		t.Fatalf("calls %q, want only a show", cards.calls)
	}
	if len(fd.posted) != 1 || fd.posted[0] != "x-4 is not waiting on the king" {
		t.Fatalf("reply %v", fd.posted)
	}
}
