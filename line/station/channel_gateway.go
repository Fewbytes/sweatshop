package station

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const (
	gatewayQuery  = "?v=10&encoding=json"
	gatewayReadSz = 1 << 20
	// Gateway intents: GUILD_MESSAGES, GUILD_MESSAGE_REACTIONS, MESSAGE_CONTENT.
	gatewayIntents = 1<<9 | 1<<10 | 1<<15

	opDispatch       = 0
	opHeartbeat      = 1
	opIdentify       = 2
	opResume         = 6
	opReconnect      = 7
	opInvalidSession = 9
	opHello          = 10
	opHeartbeatAck   = 11

	defaultGatewayBackoff = time.Second
	maxGatewayBackoff     = time.Minute
)

// Gateway configures the Discord websocket transport of a Channel.
type Gateway struct {
	Token   string        // bot token; sent only in identify and resume
	Backoff time.Duration // first reconnect delay, doubled up to a minute; default 1s
}

// gatewaySession is what a resume needs. Only the gateway goroutine touches
// the strings; seq is read by the heartbeat goroutine too.
type gatewaySession struct {
	id        string
	resumeURL string
	seq       atomic.Int64 // last dispatch sequence number; 0 before the first
}

type gatewayFrame struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  *int64          `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

// runGateway keeps a gateway session connected until ctx ends, reconnecting
// with exponential backoff. While it is down, Run's REST poller delivers.
func (c *Channel) runGateway(ctx context.Context) {
	initial := c.Gateway.Backoff
	if initial <= 0 {
		initial = defaultGatewayBackoff
	}
	backoff := initial
	for ctx.Err() == nil {
		ready, err := c.gatewayConnect(ctx)
		c.gatewayUp.Store(false)
		if ctx.Err() != nil {
			return
		}
		log.Printf("channel: gateway: %v", err)
		if ready {
			backoff = initial
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxGatewayBackoff)
	}
}

// gatewayConnect runs one websocket session and returns why it ended. ready
// says whether it got as far as being connected and backfilled.
func (c *Channel) gatewayConnect(ctx context.Context) (ready bool, err error) {
	ses := &c.gatewaySes
	endpoint := ses.resumeURL
	if ses.id == "" || endpoint == "" {
		ses.id, ses.resumeURL = "", ""
		ses.seq.Store(0)
		if endpoint, err = c.Discord.GatewayURL(ctx); err != nil {
			return false, err
		}
	}
	conn, _, err := websocket.Dial(ctx, endpoint+gatewayQuery, nil)
	if err != nil {
		return false, err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(gatewayReadSz)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var hello struct {
		HeartbeatInterval int64 `json:"heartbeat_interval"`
	}
	if f, err := readGatewayFrame(ctx, conn); err != nil {
		return false, err
	} else if f.Op != opHello || json.Unmarshal(f.D, &hello) != nil || hello.HeartbeatInterval <= 0 {
		return false, fmt.Errorf("expected hello, got op %d", f.Op)
	}
	if ses.id != "" {
		err = writeGatewayFrame(ctx, conn, opResume, map[string]any{"token": c.Gateway.Token, "session_id": ses.id, "seq": ses.seq.Load()})
	} else {
		err = writeGatewayFrame(ctx, conn, opIdentify, map[string]any{
			"token": c.Gateway.Token, "intents": gatewayIntents,
			"properties": map[string]string{"os": "linux", "browser": "sweatshop", "device": "sweatshop"},
		})
	}
	if err != nil {
		return false, err
	}

	acked := &atomic.Bool{}
	acked.Store(true)
	go c.gatewayHeartbeat(ctx, conn, cancel, time.Duration(hello.HeartbeatInterval)*time.Millisecond, acked)

	// Message events are dropped until READY/RESUMED has been backfilled: a
	// resume replays events from before the gap, and delivering them ahead of
	// the REST backfill would advance last past messages not yet fetched.
	for {
		f, err := readGatewayFrame(ctx, conn)
		if err != nil {
			return ready, err
		}
		switch f.Op {
		case opHeartbeatAck:
			acked.Store(true)
		case opHeartbeat:
			if err := writeGatewayFrame(ctx, conn, opHeartbeat, ses.seq.Load()); err != nil {
				return ready, err
			}
		case opReconnect:
			return ready, errors.New("server asked to reconnect")
		case opInvalidSession:
			var resumable bool
			_ = json.Unmarshal(f.D, &resumable)
			if !resumable {
				ses.id, ses.resumeURL = "", ""
			}
			return ready, fmt.Errorf("invalid session (resumable=%t)", resumable)
		case opDispatch:
			if f.S != nil {
				ses.seq.Store(*f.S)
			}
			switch f.T {
			case "READY":
				var d struct {
					SessionID string `json:"session_id"`
					ResumeURL string `json:"resume_gateway_url"`
					App       struct {
						ID string `json:"id"`
					} `json:"application"`
				}
				if err := json.Unmarshal(f.D, &d); err != nil || d.SessionID == "" {
					return ready, fmt.Errorf("bad READY: %v", err)
				}
				ses.id, ses.resumeURL = d.SessionID, d.ResumeURL
				if err := c.gatewayBackfill(ctx); err != nil {
					return ready, err
				}
				ready = true
				c.registerApproveCommand(ctx, d.App.ID)
			case "RESUMED":
				if err := c.gatewayBackfill(ctx); err != nil {
					return ready, err
				}
				ready = true
			case "MESSAGE_CREATE":
				if ready {
					c.gatewayMessage(ctx, f.D)
				}
			case "MESSAGE_REACTION_ADD":
				c.gatewayReaction(ctx, f.D)
			case "INTERACTION_CREATE":
				c.gatewayInteraction(ctx, f.D)
			}
		}
	}
}

// gatewayBackfill fetches over REST whatever was missed while disconnected,
// then hands delivery to the gateway.
func (c *Channel) gatewayBackfill(ctx context.Context) error {
	if err := c.Poll(ctx); err != nil {
		return fmt.Errorf("backfill: %w", err)
	}
	c.gatewayUp.Store(true)
	return nil
}

// gatewayHeartbeat sends heartbeats and abandons the connection when one goes
// unacknowledged, since a silent connection is dead.
func (c *Channel) gatewayHeartbeat(ctx context.Context, conn *websocket.Conn, abandon context.CancelFunc, every time.Duration, acked *atomic.Bool) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !acked.Swap(false) || writeGatewayFrame(ctx, conn, opHeartbeat, c.heartbeatSeq()) != nil {
			abandon()
			return
		}
	}
}

// heartbeatSeq is the heartbeat payload: the last sequence number, or null
// before the first dispatch.
func (c *Channel) heartbeatSeq() any {
	if n := c.gatewaySes.seq.Load(); n > 0 {
		return n
	}
	return nil
}

func readGatewayFrame(ctx context.Context, conn *websocket.Conn) (gatewayFrame, error) {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return gatewayFrame{}, err
	}
	var f gatewayFrame
	if err := json.Unmarshal(data, &f); err != nil {
		return gatewayFrame{}, fmt.Errorf("bad gateway frame: %w", err)
	}
	return f, nil
}

func writeGatewayFrame(ctx context.Context, conn *websocket.Conn, op int, d any) error {
	b, err := json.Marshal(map[string]any{"op": op, "d": d})
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, b)
}

// gatewayMessage delivers one MESSAGE_CREATE if it is in the line's channel.
func (c *Channel) gatewayMessage(ctx context.Context, raw json.RawMessage) {
	var m struct {
		DiscordMessage
		ChannelID string `json:"channel_id"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		log.Printf("channel: gateway message: %v", err)
		return
	}
	if m.ChannelID != c.ChannelID {
		return
	}
	c.feedMu.Lock()
	defer c.feedMu.Unlock()
	if err := c.deliver(ctx, m.DiscordMessage); err != nil {
		log.Printf("channel: gateway message %s: %v", m.ID, err)
	}
}
