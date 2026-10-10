// Package viceroy is the wake loop of the viceroy container: wait for the
// king's channel, an andon change or the heartbeat, then run one `claude -p`
// with the charter, the testament and the channel.
package viceroy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/line/station"
)

const (
	DefaultHeartbeat = 6 * time.Hour
	DefaultPoll      = 30 * time.Second
	DefaultRunLimit  = time.Hour
	historyLen       = 30
	markerName       = "last-handled-message"
)

// Message is one channel message as the station API returns it.
type Message struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	AuthorID  string `json:"author_id"`
	Principal string `json:"principal"`
	Text      string `json:"text"`
	TS        string `json:"ts"`
}

// Line is the part of the station client the loop uses.
type Line interface {
	ChannelInbox(ctx context.Context, after string, wait time.Duration) ([]byte, error)
	Status(ctx context.Context) (station.Status, error)
	TestamentGet(ctx context.Context) ([]byte, bool, error)
	TestamentPut(ctx context.Context, body []byte) error
}

// Agent runs one prompt to completion and returns its output.
type Agent interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// ClaudeCLI runs `claude -p` with the station's settings, the same flags as
// station/entrypoint.sh.
type ClaudeCLI struct {
	Bin     string // default "claude"
	Dir     string // working directory (the read-only clone)
	Timeout time.Duration
}

func (c ClaudeCLI) Run(ctx context.Context, prompt string) (string, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	bin := c.Bin
	if bin == "" {
		bin = "claude"
	}
	cmd := exec.CommandContext(ctx, bin, "-p", prompt,
		"--setting-sources", "user",
		"--settings", "/etc/sweatshop/claude-settings.json",
		"--strict-mcp-config",
		"--dangerously-skip-permissions",
		"--output-format", "text")
	cmd.Dir = c.Dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// Loop is the viceroy's wake loop.
type Loop struct {
	Line        Line
	Agent       Agent
	Gen         string
	Predecessor string
	Heartbeat   time.Duration // default DefaultHeartbeat
	Poll        time.Duration // inbox long-poll per round; default DefaultPoll
	Backoff     time.Duration // pause after a failed round; default 10s
	Log         *log.Logger
	Now         func() time.Time // default time.Now

	// OnRun, if set, is called after each completed run (for tests).
	OnRun func()
}

func (l *Loop) logf(format string, a ...any) {
	if l.Log != nil {
		l.Log.Printf(format, a...)
	}
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// Run loops until ctx is done.
func (l *Loop) Run(ctx context.Context) error {
	now := l.Now
	if now == nil {
		now = time.Now
	}
	heartbeat := orDefault(l.Heartbeat, DefaultHeartbeat)
	poll := orDefault(l.Poll, DefaultPoll)
	backoff := orDefault(l.Backoff, 10*time.Second)

	// Fetch the whole inbox once; what the testament marks as handled is
	// history, the rest is the first batch.
	testament, _, err := l.Line.TestamentGet(ctx)
	if err != nil {
		return fmt.Errorf("read testament: %w", err)
	}
	handled := Marker(string(testament))
	all, err := l.fetchAll(ctx, "", 0)
	if err != nil {
		return err
	}
	var history, pending []Message // history keeps the last historyLen
	for _, m := range all {
		if handled != "" && !idLess(handled, m.ID) {
			history = append(history, m)
			if len(history) > historyLen {
				history = history[1:]
			}
		} else {
			pending = append(pending, m)
		}
	}
	st, err := l.Line.Status(ctx)
	if err != nil {
		return fmt.Errorf("read status: %w", err)
	}
	lastEvent := st.Event
	nextBeat := now().Add(heartbeat)
	wake := WakeStartup

	for {
		if wake == "" {
			// Wait for the first of: new messages, an andon change, the heartbeat.
			for wake == "" {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if len(pending) > 0 {
					wake = WakeMessages
					break
				}
				w := min(poll, max(nextBeat.Sub(now()), 0))
				msgs, err := l.fetch(ctx, handled, w)
				switch {
				case ctx.Err() != nil:
					return ctx.Err()
				case err != nil:
					l.logf("inbox: %v", err)
					sleep(ctx, backoff)
					continue
				}
				pending = append(pending, msgs...)
				if len(pending) > 0 {
					wake = WakeMessages
					break
				}
				if s, err := l.Line.Status(ctx); err != nil {
					l.logf("status: %v", err)
				} else if s.Event != lastEvent {
					wake = WakeAndon
				}
				if wake == "" && !now().Before(nextBeat) {
					wake = WakeHeartbeat
				}
			}
		}

		cur, err := l.Line.Status(ctx)
		if err != nil {
			l.logf("status: %v", err)
			sleep(ctx, backoff)
			continue
		}
		tbody, _, err := l.Line.TestamentGet(ctx)
		if err != nil {
			l.logf("testament: %v", err)
			sleep(ctx, backoff)
			continue
		}
		batch := pending
		out, runErr := l.Agent.Run(ctx, Prompt(PromptInput{
			Wake: wake, Gen: l.Gen, Predecessor: l.Predecessor,
			Testament: string(tbody), History: history, New: batch, Status: cur,
		}))
		if runErr != nil {
			l.logf("claude: %v", runErr)
		}
		l.logf("run (%s, %d new messages) output:\n%s", wake, len(batch), strings.TrimSpace(out))

		// Record the batch as handled even if the run failed: a poison batch
		// must not wedge the loop. The marker goes in the freshest testament,
		// which the run may have rewritten.
		if n := len(batch); n > 0 {
			handled = batch[n-1].ID
			history = append(history, batch...)
			if len(history) > historyLen {
				history = history[len(history)-historyLen:]
			}
			pending = nil
			if err := l.recordMarker(context.WithoutCancel(ctx), handled); err != nil {
				l.logf("record marker: %v", err)
			}
		}
		lastEvent = cur.Event
		nextBeat = now().Add(heartbeat)
		wake = ""
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if l.OnRun != nil {
			l.OnRun()
		}
		if runErr != nil {
			sleep(ctx, backoff)
		}
	}
}

func (l *Loop) recordMarker(ctx context.Context, id string) error {
	body, _, err := l.Line.TestamentGet(ctx)
	if err != nil {
		return err
	}
	return l.Line.TestamentPut(ctx, []byte(SetMarker(string(body), id)))
}

func (l *Loop) fetch(ctx context.Context, after string, wait time.Duration) ([]Message, error) {
	raw, err := l.Line.ChannelInbox(ctx, after, wait)
	if err != nil {
		return nil, err
	}
	var msgs []Message
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return nil, fmt.Errorf("decode inbox: %w", err)
	}
	return msgs, nil
}

// fetchAll pages through the inbox from after until it is exhausted.
func (l *Loop) fetchAll(ctx context.Context, after string, wait time.Duration) ([]Message, error) {
	var all []Message
	for {
		page, err := l.fetch(ctx, after, wait)
		if err != nil {
			return nil, fmt.Errorf("read inbox: %w", err)
		}
		if len(page) == 0 {
			return all, nil
		}
		all = append(all, page...)
		after = page[len(page)-1].ID
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// idLess orders Discord snowflake IDs (decimal strings).
func idLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

var markerRe = regexp.MustCompile(`(?m)^` + markerName + `: *(\d+) *$`)

// Marker returns the last handled message ID recorded in a testament, or "".
func Marker(testament string) string {
	if m := markerRe.FindAllStringSubmatch(testament, -1); len(m) > 0 {
		return m[len(m)-1][1]
	}
	return ""
}

// SetMarker returns the testament with its marker line set to id.
func SetMarker(testament, id string) string {
	line := markerName + ": " + id
	if markerRe.MatchString(testament) {
		i := 0
		return markerRe.ReplaceAllStringFunc(testament, func(string) string {
			// Keep one marker only: the first is rewritten, the rest dropped.
			if i++; i > 1 {
				return ""
			}
			return line
		})
	}
	if testament != "" && !strings.HasSuffix(testament, "\n") {
		testament += "\n"
	}
	return testament + line + "\n"
}
