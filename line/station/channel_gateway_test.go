package station

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// gwConn is one websocket connection accepted by fakeGateway.
type gwConn struct {
	conn   *websocket.Conn
	ctx    context.Context
	frames chan gatewayFrame // frames the client sent, heartbeats included
}

func (g *gwConn) send(t *testing.T, op int, ev string, seq int64, d any) {
	t.Helper()
	b, _ := json.Marshal(d)
	f := gatewayFrame{Op: op, D: b, T: ev}
	if seq > 0 {
		f.S = &seq
	}
	out, _ := json.Marshal(f)
	if err := g.conn.Write(g.ctx, websocket.MessageText, out); err != nil {
		t.Fatalf("gateway send: %v", err)
	}
}

func (g *gwConn) next(t *testing.T, op int) gatewayFrame {
	t.Helper()
	for {
		select {
		case f := <-g.frames:
			if f.Op == op {
				return f
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no op %d frame from client", op)
		}
	}
}

// fakeGateway is a fakeDiscordServer that also serves the gateway: /gateway/bot,
// a websocket at /ws, and the REST calls the approvals make.
type fakeGateway struct {
	*fakeDiscordServer
	conns chan *gwConn
	url   string
	// refuse makes /ws fail, as if Discord's gateway were down.
	refuse atomic.Bool
	// silent makes the gateway ignore heartbeats instead of acking them.
	silent atomic.Bool

	cmu       sync.Mutex
	callbacks []string // "<interaction id> <json body>"
	commands  []string // "<app>/<guild>"
	typings   int
}

func (f *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/gateway/bot":
		_, _ = w.Write([]byte(`{"url":"` + f.url + `/ws"}`))
	case r.URL.Path == "/ws":
		f.serveWS(w, r)
	case r.URL.Path == "/channels/555" && r.Method == http.MethodGet:
		_, _ = w.Write([]byte(`{"guild_id":"G1"}`))
	case r.URL.Path == "/applications/A1/guilds/G1/commands":
		f.cmu.Lock()
		f.commands = append(f.commands, "A1/G1")
		f.cmu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	case strings.HasPrefix(r.URL.Path, "/interactions/"):
		var body json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.cmu.Lock()
		f.callbacks = append(f.callbacks, strings.Split(r.URL.Path, "/")[2]+" "+string(body))
		f.cmu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/channels/555/typing":
		f.cmu.Lock()
		f.typings++
		f.cmu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(r.URL.Path, "/channels/555/messages/"):
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, m := range f.msgs {
			if m["id"] == strings.TrimPrefix(r.URL.Path, "/channels/555/messages/") {
				_ = json.NewEncoder(w).Encode(m)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	default:
		f.fakeDiscordServer.ServeHTTP(w, r)
	}
}

func (f *fakeGateway) serveWS(w http.ResponseWriter, r *http.Request) {
	if f.refuse.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	g := &gwConn{conn: conn, ctx: r.Context(), frames: make(chan gatewayFrame, 64)}
	// Long enough that a client busy with a REST backfill still acks in time under load.
	hello, _ := json.Marshal(map[string]int{"heartbeat_interval": 250})
	out, _ := json.Marshal(gatewayFrame{Op: opHello, D: hello})
	_ = conn.Write(g.ctx, websocket.MessageText, out)
	f.conns <- g
	for {
		_, data, err := conn.Read(g.ctx)
		if err != nil {
			return
		}
		var fr gatewayFrame
		_ = json.Unmarshal(data, &fr)
		g.frames <- fr
		if fr.Op == opHeartbeat && !f.silent.Load() {
			_ = conn.Write(g.ctx, websocket.MessageText, []byte(`{"op":11}`))
		}
	}
}

func (f *fakeGateway) callbackLog() []string {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	return append([]string(nil), f.callbacks...)
}

// proposedCards is a Cards whose List answers by label.
type proposedCards struct {
	*fakeCards
	all, proposed string
}

func (p proposedCards) List(_ context.Context, l ListFilter) (json.RawMessage, error) {
	if l.Label == LabelProposed {
		return json.RawMessage(p.proposed), nil
	}
	return json.RawMessage(p.all), nil
}

func newGatewayChan(t *testing.T, cards Cards) (*Channel, *fakeGateway) {
	t.Helper()
	fg := &fakeGateway{fakeDiscordServer: &fakeDiscordServer{}, conns: make(chan *gwConn, 8)}
	ts := httptest.NewServer(fg)
	t.Cleanup(ts.Close)
	fg.url = "ws" + strings.TrimPrefix(ts.URL, "http")
	ch := &Channel{
		Dir: filepath.Join(t.TempDir(), "channel"), ChannelID: "555", King: kingID,
		Discord: HTTPDiscord{Base: ts.URL, Token: "tok"}, Cards: cards,
		Gateway:  &Gateway{Token: "tok", Backoff: 5 * time.Millisecond},
		Interval: 10 * time.Millisecond,
	}
	return ch, fg
}

func runChannel(t *testing.T, ch *Channel) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { ch.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func (f *fakeGateway) accept(t *testing.T) *gwConn {
	t.Helper()
	select {
	case g := <-f.conns:
		return g
	case <-time.After(5 * time.Second):
		t.Fatal("client did not connect to the gateway")
		return nil
	}
}

func messageCreate(id, channel, author, text string) map[string]any {
	return map[string]any{"id": id, "channel_id": channel, "content": text, "timestamp": "2026-10-10T00:00:00Z",
		"author": map[string]any{"id": author, "username": "u" + author}}
}

func waitInbox(t *testing.T, ch *Channel, wantIDs ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		msgs, _ := ch.Since(context.Background(), "", 0)
		var ids []string
		for _, m := range msgs {
			ids = append(ids, m.ID)
		}
		if strings.Join(ids, ",") == strings.Join(wantIDs, ",") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("inbox ids %v, want %v", ids, wantIDs)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestGatewayIdentifyHeartbeatAndMessageDedupe(t *testing.T) {
	ch, fg := newGatewayChan(t, nil)
	fg.add("100", "1", "history")
	if err := ch.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	runChannel(t, ch)
	g := fg.accept(t)
	var id struct {
		Token      string `json:"token"`
		Intents    int    `json:"intents"`
		Properties map[string]string
	}
	if err := json.Unmarshal(g.next(t, opIdentify).D, &id); err != nil || id.Token != "tok" || id.Intents != 1<<9|1<<10|1<<15 {
		t.Fatalf("identify %+v %v", id, err)
	}
	g.send(t, opDispatch, "READY", 1, map[string]any{"session_id": "s1", "resume_gateway_url": fg.url + "/ws", "application": map[string]string{"id": "A1"}})
	g.send(t, opDispatch, "MESSAGE_CREATE", 2, messageCreate("101", "555", "2", "hello"))
	g.send(t, opDispatch, "MESSAGE_CREATE", 3, messageCreate("101", "555", "2", "hello again"))
	g.send(t, opDispatch, "MESSAGE_CREATE", 4, messageCreate("102", "999", "2", "other channel"))
	waitInbox(t, ch, "101")
	if hb := g.next(t, opHeartbeat); string(hb.D) == "" {
		t.Fatal("heartbeat has no payload")
	}
	g.send(t, opDispatch, "MESSAGE_CREATE", 5, messageCreate("103", "555", "2", "later"))
	waitInbox(t, ch, "101", "103")
	if got, _ := ch.Since(context.Background(), "", 0); got[0].Text != "hello" {
		t.Fatalf("duplicate replaced the first delivery: %+v", got)
	}
	fg.cmu.Lock()
	defer fg.cmu.Unlock()
	if len(fg.commands) != 2 { // /approve and /card, once each
		t.Fatalf("slash command registrations: %v", fg.commands)
	}
}

func TestGatewayReconnectResumesAndBackfillsWithoutDuplicates(t *testing.T) {
	ch, fg := newGatewayChan(t, nil)
	fg.add("100", "1", "history")
	if err := ch.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	runChannel(t, ch)
	g1 := fg.accept(t)
	g1.next(t, opIdentify)
	g1.send(t, opDispatch, "READY", 1, map[string]any{"session_id": "s1", "resume_gateway_url": fg.url + "/ws", "application": map[string]string{"id": "A1"}})
	g1.send(t, opDispatch, "MESSAGE_CREATE", 2, messageCreate("101", "555", "2", "one"))
	waitInbox(t, ch, "101")

	// 102 arrives while the gateway is gone; only the REST backfill can see it.
	fg.add("102", "2", "missed")
	g1.send(t, opReconnect, "", 0, nil)
	g2 := fg.accept(t)
	var resume struct {
		Token     string `json:"token"`
		SessionID string `json:"session_id"`
		Seq       int64  `json:"seq"`
	}
	if err := json.Unmarshal(g2.next(t, opResume).D, &resume); err != nil || resume.SessionID != "s1" || resume.Seq != 2 || resume.Token != "tok" {
		t.Fatalf("resume %+v %v", resume, err)
	}
	g2.send(t, opDispatch, "RESUMED", 3, map[string]any{})
	waitInbox(t, ch, "101", "102")
	// The gateway now replays 102 and delivers a new message: no duplicate.
	g2.send(t, opDispatch, "MESSAGE_CREATE", 4, messageCreate("102", "555", "2", "missed"))
	g2.send(t, opDispatch, "MESSAGE_CREATE", 5, messageCreate("103", "555", "2", "three"))
	waitInbox(t, ch, "101", "102", "103")
}

func TestGatewayInvalidSessionIdentifiesAgain(t *testing.T) {
	ch, fg := newGatewayChan(t, nil)
	if err := ch.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	runChannel(t, ch)
	g1 := fg.accept(t)
	g1.next(t, opIdentify)
	g1.send(t, opDispatch, "READY", 1, map[string]any{"session_id": "s1", "resume_gateway_url": fg.url + "/ws", "application": map[string]string{"id": "A1"}})
	g1.send(t, opInvalidSession, "", 0, false)
	fg.accept(t).next(t, opIdentify)
}

func TestGatewayReconnectsWhenHeartbeatIsNotAcked(t *testing.T) {
	ch, fg := newGatewayChan(t, nil)
	fg.silent.Store(true)
	if err := ch.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	runChannel(t, ch)
	g1 := fg.accept(t)
	g1.next(t, opIdentify)
	fg.accept(t) // the silent connection was abandoned
}

func TestChannelPollsWhileGatewayIsDown(t *testing.T) {
	ch, fg := newGatewayChan(t, nil)
	fg.refuse.Store(true)
	fg.add("100", "1", "history")
	if err := ch.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	runChannel(t, ch)
	fg.add("101", "2", "via rest")
	waitInbox(t, ch, "101")
}

func TestGatewayInteractionApprovesForSovereignOnly(t *testing.T) {
	cards := &fakeCards{}
	ch, fg := newGatewayChan(t, proposedCards{fakeCards: cards,
		proposed: `[{"id":"x-1","title":"Add widgets"},{"id":"x-2","title":"Fix gadgets"}]`})
	ctx := context.Background()
	interaction := func(typ int, user, card string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{
			"id": "I" + strconv.Itoa(typ) + user, "token": "tk", "type": typ, "channel_id": "555",
			"member": map[string]any{"user": map[string]string{"id": user}},
			"data":   map[string]any{"name": "approve", "options": []map[string]any{{"name": "card", "value": card}}},
		})
		return b
	}

	ch.gatewayInteraction(ctx, interaction(interactionApplicationCommand, "666", "x-1"))
	ch.gatewayInteraction(ctx, interaction(interactionAutocomplete, "666", "x"))
	if len(cards.calls) != 0 || len(fg.callbackLog()) != 0 {
		t.Fatalf("non-sovereign interaction acted: calls %v callbacks %v", cards.calls, fg.callbackLog())
	}

	ch.gatewayInteraction(ctx, interaction(interactionApplicationCommand, kingID, "x-1"))
	if got := strings.Join(cards.calls, "|"); got != "unlabel x-1 line-gave-up|label x-1 line|comment x-1 approved by the king (discord interaction I2477)" {
		t.Fatalf("approve calls %q", got)
	}
	cb := fg.callbackLog()
	if len(cb) != 1 || !strings.HasPrefix(cb[0], "I2477 ") || !strings.Contains(cb[0], `"approved x-1"`) || !strings.Contains(cb[0], `"flags":64`) {
		t.Fatalf("ephemeral ack %v", cb)
	}

	ch.gatewayInteraction(ctx, interaction(interactionAutocomplete, kingID, "gadget"))
	cb = fg.callbackLog()
	if len(cb) != 2 || !strings.Contains(cb[1], `"value":"x-2"`) || strings.Contains(cb[1], `"x-1"`) || !strings.Contains(cb[1], `"name":"x-2 Fix gadgets"`) {
		t.Fatalf("autocomplete %v", cb)
	}

	ch.gatewayInteraction(ctx, interaction(interactionApplicationCommand, kingID, "../etc"))
	if cb = fg.callbackLog(); len(cb) != 3 || !strings.Contains(cb[2], "invalid card id") || len(cards.calls) != 3 {
		t.Fatalf("invalid id: %v calls %v", cb, cards.calls)
	}
}

func TestGatewayReactionApprovesOnlyPostsNamingOneCard(t *testing.T) {
	cards := &fakeCards{}
	ch, fg := newGatewayChan(t, proposedCards{fakeCards: cards, all: `[{"id":"x-1"},{"id":"x-2"},{"id":"x-3"}]`})
	ctx := context.Background()
	fg.add("200", "999", "Please approve x-1.\n-# viceroy gen 1 @abc1234") // bot post, one card
	fg.add("201", "999", "x-1 and x-2 are both ready")                     // two cards
	fg.add("202", "999", "nothing proposed today, well-known")             // zero cards
	fg.add("203", "5", "x-3 from a human")                                 // not the bot's post
	fg.add("204", "999", "x-1 again, also x-1")                            // one distinct card
	if err := ch.Poll(ctx); err != nil {                                   // learn the bot's ID
		t.Fatal(err)
	}
	react := func(user, msg, emoji string) {
		b, _ := json.Marshal(map[string]any{"user_id": user, "channel_id": "555", "message_id": msg, "emoji": map[string]string{"name": emoji}})
		ch.gatewayReaction(ctx, b)
	}

	react(kingID, "201", "✅")
	react(kingID, "202", "✅")
	react(kingID, "203", "✅")
	react(kingID, "200", "👍")
	react("666", "200", "✅")
	if len(cards.calls) != 0 {
		t.Fatalf("ignored reactions approved: %v", cards.calls)
	}

	react(kingID, "200", "✅")
	if got := strings.Join(cards.calls, "|"); got != "unlabel x-1 line-gave-up|label x-1 line|comment x-1 approved by the king (discord reaction on message 200)" {
		t.Fatalf("approve calls %q", got)
	}
	if posted := fg.posted; len(posted) != 1 || posted[0] != "approved x-1" {
		t.Fatalf("replies %v", posted)
	}
	react(kingID, "204", "✅")
	if len(cards.calls) != 6 {
		t.Fatalf("a post repeating one card should approve it: %v", cards.calls)
	}
}

func TestChannelTypingRoute(t *testing.T) {
	ch, fg := newGatewayChan(t, nil)
	v, m := channelRig(t, ch)
	ctx := context.Background()
	if err := v.ChannelTyping(ctx); err != nil {
		t.Fatal(err)
	}
	if fg.typings != 1 {
		t.Fatalf("typings = %d", fg.typings)
	}
	if err := m.ChannelTyping(ctx); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("maker typing: %v", err)
	}
}

func TestGatewayCardCommandFilesCardForSovereignOnly(t *testing.T) {
	cards := &fakeCards{}
	ch, fg := newGatewayChan(t, cards)
	ctx := context.Background()
	card := func(user string, options ...map[string]any) json.RawMessage {
		b, _ := json.Marshal(map[string]any{
			"id": "I9" + user, "token": "tk", "type": interactionApplicationCommand, "channel_id": "555",
			"member": map[string]any{"user": map[string]string{"id": user}},
			"data":   map[string]any{"name": "card", "options": options},
		})
		return b
	}
	opt := func(name string, value any) map[string]any { return map[string]any{"name": name, "value": value} }

	ch.gatewayInteraction(ctx, card("666", opt("title", "Add widgets")))
	if len(cards.calls) != 0 || len(fg.callbackLog()) != 0 {
		t.Fatalf("non-sovereign /card acted: calls %v callbacks %v", cards.calls, fg.callbackLog())
	}

	ch.gatewayInteraction(ctx, card(kingID, opt("title", "Add widgets")))
	want := NewCard{Title: "Add widgets", Type: "task", Priority: 2}
	if cards.last != want {
		t.Fatalf("defaults: created %+v, want %+v", cards.last, want)
	}
	if got := strings.Join(cards.calls, "|"); got != "create Add widgets|unlabel x-2 line-gave-up|label x-2 line|comment x-2 approved by the king (discord interaction I9"+kingID+")" {
		t.Fatalf("file calls %q", got)
	}
	if cb := fg.callbackLog(); len(cb) != 1 || !strings.Contains(cb[0], `"filed x-2"`) || !strings.Contains(cb[0], `"flags":64`) {
		t.Fatalf("ephemeral ack %v", cb)
	}

	ch.gatewayInteraction(ctx, card(kingID, opt("title", "Fix gadgets"), opt("description", "they jam"), opt("type", "bug"), opt("priority", 0)))
	want = NewCard{Title: "Fix gadgets", Description: "they jam", Type: "bug", Priority: 0}
	if cards.last != want {
		t.Fatalf("explicit options: created %+v, want %+v", cards.last, want)
	}

	before := len(cards.calls)
	ch.gatewayInteraction(ctx, card(kingID, opt("title", "Bad"), opt("type", "saga")))
	if cb := fg.callbackLog(); len(cards.calls) != before || !strings.Contains(cb[len(cb)-1], "invalid card") {
		t.Fatalf("invalid type: calls %v callbacks %v", cards.calls, cb)
	}
}
