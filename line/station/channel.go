package station

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Fewbytes/sweatshop/line/constitution"
)

const (
	maxPostChars = 2000
	// maxFooterChars is the room kept free in a post for the metadata footer.
	maxFooterChars = 100
	maxSourceChars = 40
	maxWait        = 60 * time.Second
	pollEvery      = 3 * time.Second
	inboxPage      = 200
	discordBase    = "https://discord.com/api/v10"
)

var approveRe = regexp.MustCompile(`^approve ([a-z0-9-]+(\.[0-9]+)*)$`)
var digitsRe = regexp.MustCompile(`^[0-9]{1,24}$`)

// DiscordMessage is the part of a Discord message the line uses.
type DiscordMessage struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
	Author    struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"author"`
}

// RateLimitError is a Discord 429; the poller waits After before retrying.
type RateLimitError struct{ After time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("discord rate limited, retry after %s", e.After)
}

// Discord is the channel transport. Production uses HTTPDiscord; tests use a
// fake server behind it.
type Discord interface {
	Me(ctx context.Context) (string, error)
	// Messages returns messages newer than after ("" for the latest page) in
	// any order.
	Messages(ctx context.Context, channel, after string) ([]DiscordMessage, error)
	Post(ctx context.Context, channel, text string) error
}

// HTTPDiscord speaks the Discord REST API with a bot token.
type HTTPDiscord struct {
	Base   string // default https://discord.com/api/v10
	Token  string
	Client *http.Client
}

func (d HTTPDiscord) do(ctx context.Context, method, path string, body any, out any) error {
	base := d.Base
	if base == "" {
		base = discordBase
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+d.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	cl := d.Client
	if cl == nil {
		cl = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		var rl struct {
			RetryAfter float64 `json:"retry_after"`
		}
		_ = json.Unmarshal(data, &rl)
		if rl.RetryAfter <= 0 {
			rl.RetryAfter, _ = strconv.ParseFloat(resp.Header.Get("Retry-After"), 64)
		}
		if rl.RetryAfter <= 0 {
			rl.RetryAfter = 1
		}
		return &RateLimitError{After: time.Duration(rl.RetryAfter * float64(time.Second))}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("discord %s %s: %s", method, path, resp.Status)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (d HTTPDiscord) Me(ctx context.Context) (string, error) {
	var u struct {
		ID string `json:"id"`
	}
	if err := d.do(ctx, http.MethodGet, "/users/@me", nil, &u); err != nil {
		return "", err
	}
	if u.ID == "" {
		return "", errors.New("discord /users/@me returned no id")
	}
	return u.ID, nil
}

func (d HTTPDiscord) Messages(ctx context.Context, channel, after string) ([]DiscordMessage, error) {
	q := url.Values{"limit": {"100"}}
	if after != "" {
		q.Set("after", after)
	} else {
		q.Set("limit", "1")
	}
	var msgs []DiscordMessage
	err := d.do(ctx, http.MethodGet, "/channels/"+url.PathEscape(channel)+"/messages?"+q.Encode(), nil, &msgs)
	return msgs, err
}

func (d HTTPDiscord) Post(ctx context.Context, channel, text string) error {
	return d.do(ctx, http.MethodPost, "/channels/"+url.PathEscape(channel)+"/messages",
		map[string]any{"content": text, "allowed_mentions": map[string]any{"parse": []string{}}}, nil)
}

// InboxMessage is one line of channel/inbox.jsonl.
type InboxMessage struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	AuthorID  string `json:"author_id"`
	Principal string `json:"principal"` // "king" or "unknown"
	Text      string `json:"text"`
	TS        string `json:"ts"`
}

// Channel polls the line's Discord channel into an inbox and applies the
// king's commands. Only the author ID decides who is the king.
type Channel struct {
	Dir       string // <line dir>/channel
	ChannelID string
	King      string // sovereign Discord user ID; "" means no king
	Discord   Discord
	Cards     Cards // nil: approve replies that cards are off
	Interval  time.Duration
	// Constitution applies `approve constitution <n>`; Restart then makes
	// serve reload. Nil: the command replies that it is off.
	Constitution func(ctx context.Context, n int, msgID string) error
	Restart      func()

	mu     sync.Mutex
	me     string
	last   string
	wake   chan struct{} // closed and replaced when the inbox grows
	loaded bool
}

// OpenChannel builds the channel for a line directory. It returns nil (the
// channel is off) when the constitution has no channel or discord.cred is
// missing.
func OpenChannel(lineDir string, c constitution.Constitution, cards Cards) (*Channel, error) {
	if c.Channel.Channel == "" {
		return nil, nil
	}
	cred := filepath.Join(lineDir, "discord.cred")
	info, err := os.Stat(cred)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s has mode %o; want 0600", cred, info.Mode().Perm())
	}
	tok, err := os.ReadFile(cred)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(string(tok))
	if token == "" {
		return nil, fmt.Errorf("%s is empty", cred)
	}
	return &Channel{
		Dir: filepath.Join(lineDir, "channel"), ChannelID: c.Channel.Channel, King: c.Sovereign.Discord,
		Discord: HTTPDiscord{Token: token}, Cards: cards,
	}, nil
}

func (c *Channel) inboxPath() string { return filepath.Join(c.Dir, "inbox.jsonl") }
func (c *Channel) lastPath() string  { return filepath.Join(c.Dir, "last_id") }

func (c *Channel) init() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		return nil
	}
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	b, err := os.ReadFile(c.lastPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	c.last = strings.TrimSpace(string(b))
	c.wake = make(chan struct{})
	c.loaded = true
	return nil
}

// snowflakeLess orders Discord IDs numerically.
func snowflakeLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// Run polls until ctx ends.
func (c *Channel) Run(ctx context.Context) {
	every := c.Interval
	if every <= 0 {
		every = pollEvery
	}
	for ctx.Err() == nil {
		wait := every
		if err := c.Poll(ctx); err != nil {
			var rl *RateLimitError
			if errors.As(err, &rl) {
				wait = rl.After
			}
			if ctx.Err() == nil {
				log.Printf("channel: poll: %v", err)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
}

// Poll fetches new messages once, appends them to the inbox and applies king
// commands. On first start without saved state it only records the newest
// message ID, so history is never replayed.
func (c *Channel) Poll(ctx context.Context) error {
	if err := c.init(); err != nil {
		return err
	}
	if c.me == "" {
		me, err := c.Discord.Me(ctx)
		if err != nil {
			return err
		}
		c.me = me
	}
	msgs, err := c.Discord.Messages(ctx, c.ChannelID, c.last)
	if err != nil {
		return err
	}
	slices.SortFunc(msgs, func(a, b DiscordMessage) int {
		switch {
		case snowflakeLess(a.ID, b.ID):
			return -1
		case snowflakeLess(b.ID, a.ID):
			return 1
		}
		return 0
	})
	if c.last == "" {
		if n := len(msgs); n > 0 {
			return c.saveLast(msgs[n-1].ID)
		}
		return c.saveLast(nowSnowflake())
	}
	for _, m := range msgs {
		if !snowflakeLess(c.last, m.ID) {
			continue
		}
		if m.Author.ID != c.me {
			im := InboxMessage{ID: m.ID, Author: m.Author.Username, AuthorID: m.Author.ID, Principal: "unknown", Text: m.Content, TS: m.Timestamp}
			if c.King != "" && m.Author.ID == c.King {
				im.Principal = "king"
			}
			if err := c.append(im); err != nil {
				return err
			}
			if im.Principal == "king" {
				c.command(ctx, im)
			}
		}
		if err := c.saveLast(m.ID); err != nil {
			return err
		}
	}
	return nil
}

// nowSnowflake is a synthetic Discord ID for the current time, used as the
// baseline when the channel is empty so later messages are delivered.
func nowSnowflake() string {
	return strconv.FormatInt((time.Now().UnixMilli()-1420070400000)<<22, 10)
}

func (c *Channel) saveLast(id string) error {
	tmp := c.lastPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.lastPath()); err != nil {
		return err
	}
	c.last = id
	return nil
}

func (c *Channel) append(m InboxMessage) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(c.inboxPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	c.mu.Lock()
	close(c.wake)
	c.wake = make(chan struct{})
	c.mu.Unlock()
	return nil
}

// command applies a king message. The caller has already checked the author.
func (c *Channel) command(ctx context.Context, m InboxMessage) {
	if sub := approveConstitutionRe.FindStringSubmatch(m.Text); sub != nil {
		c.approveConstitution(ctx, m, sub[1])
		return
	}
	sub := approveRe.FindStringSubmatch(m.Text)
	if sub == nil {
		return
	}
	id := sub[1]
	reply := func(format string, args ...any) {
		if err := c.Discord.Post(ctx, c.ChannelID, fmt.Sprintf(format, args...)); err != nil {
			log.Printf("channel: reply: %v", err)
		}
	}
	if c.Cards == nil {
		reply("cannot approve %s: cards are not configured", id)
		return
	}
	if err := c.Cards.AddLabel(ctx, id, LabelLine); err != nil {
		log.Printf("channel: approve %s: %v", id, err)
		reply("could not approve %s", id)
		return
	}
	if err := c.Cards.Comment(ctx, id, "approved by the king (discord message "+m.ID+")"); err != nil {
		log.Printf("channel: approve %s comment: %v", id, err)
	}
	reply("approved %s", id)
}

func (c *Channel) approveConstitution(ctx context.Context, m InboxMessage, num string) {
	reply := func(format string, args ...any) {
		if err := c.Discord.Post(ctx, c.ChannelID, fmt.Sprintf(format, args...)); err != nil {
			log.Printf("channel: reply: %v", err)
		}
	}
	// Defence in depth: Poll only calls command for the king.
	if c.King == "" || m.AuthorID != c.King {
		return
	}
	n, _ := strconv.Atoi(num)
	if c.Constitution == nil {
		reply("cannot approve constitution %d: not configured", n)
		return
	}
	if err := c.Constitution(ctx, n, m.ID); err != nil {
		log.Printf("channel: approve constitution %d: %v", n, err)
		reply("could not approve constitution %d: %v", n, err)
		return
	}
	reply("constitution proposal %d approved; reloading", n)
	if c.Restart != nil {
		c.Restart()
	}
}

// Since returns inbox messages with ID after `after`, oldest first, waiting up
// to wait for the first one.
func (c *Channel) Since(ctx context.Context, after string, wait time.Duration) ([]InboxMessage, error) {
	if err := c.init(); err != nil {
		return nil, err
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		c.mu.Lock()
		wake := c.wake
		c.mu.Unlock()
		msgs, err := c.readInbox(after)
		if err != nil || len(msgs) > 0 || wait <= 0 {
			return msgs, err
		}
		select {
		case <-wake:
		case <-deadline.C:
			return msgs, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (c *Channel) readInbox(after string) ([]InboxMessage, error) {
	f, err := os.Open(c.inboxPath())
	if errors.Is(err, os.ErrNotExist) {
		return []InboxMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := []InboxMessage{}
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m InboxMessage
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if after == "" || snowflakeLess(after, m.ID) {
			out = append(out, m)
			if len(out) == inboxPage {
				break
			}
		}
	}
	return out, sc.Err()
}

// routeChannel handles /v1/channel/inbox and /v1/channel/post (viceroy only).
func (s *Server) routeChannel(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	path := r.URL.Path
	want := http.MethodGet
	if path == "/v1/channel/post" {
		want = http.MethodPost
	} else if path != "/v1/channel/inbox" {
		return "", fail(http.StatusNotFound, "no route %s", path)
	}
	if r.Method != want {
		w.Header().Set("Allow", want)
		return "", fail(http.StatusMethodNotAllowed, "use %s for %s", want, path)
	}
	if st.Role != viceroyRole {
		return "", fail(http.StatusForbidden, "role %q may not use the channel", st.Role)
	}
	ch := s.Channel
	if ch == nil {
		return "", fail(http.StatusServiceUnavailable, "channel is off (no discord.cred)")
	}
	if want == http.MethodPost {
		if s.Viceroy != nil {
			if err := s.Viceroy.Admit(r, true); err != nil {
				return "", err
			}
		}
		var body struct {
			Text string `json:"text"`
			From string `json:"from"`
		}
		if err := decodeBody(w, r, &body); err != nil {
			return "", err
		}
		n := utf8.RuneCountInString(body.Text)
		if strings.TrimSpace(body.Text) == "" || n > maxPostChars-maxFooterChars || !utf8.ValidString(body.Text) || !utf8.ValidString(body.From) {
			return "", fail(http.StatusBadRequest, "text must be 1..%d characters", maxPostChars-maxFooterChars)
		}
		// A master that cannot be read must not silence the viceroy: the
		// footer just omits the sha.
		master, _ := s.Guard.Repo.Ref(r.Context(), s.Guard.Ref)
		text := body.Text + postFooter(r.Header.Get(GenHeader), master, body.From)
		if err := ch.Discord.Post(r.Context(), ch.ChannelID, text); err != nil {
			return "", fail(http.StatusBadGateway, "post: %v", err)
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return fmt.Sprintf("post %d chars", n), nil
	}
	q := r.URL.Query()
	after := q.Get("after")
	if after != "" && !digitsRe.MatchString(after) {
		return "", fail(http.StatusBadRequest, "invalid after %q", after)
	}
	var wait time.Duration
	if v := q.Get("wait"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			secs, serr := strconv.Atoi(v)
			if serr != nil {
				return "", fail(http.StatusBadRequest, "invalid wait %q", v)
			}
			d = time.Duration(secs) * time.Second
		}
		wait = min(max(d, 0), maxWait)
	}
	msgs, err := ch.Since(r.Context(), after, wait)
	if err != nil {
		return "", err
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
	return fmt.Sprintf("inbox %d messages", len(msgs)), nil
}

// postFooter is the one-line speaker footer the station appends to every
// viceroy channel post: role, generation and running master sha, plus whose
// message it is when the viceroy is relaying someone else's.
func postFooter(gen, master, from string) string {
	f := "\n-# " + viceroyRole
	if gen = strings.TrimSpace(gen); gen != "" {
		f += " gen " + gen
	}
	if len(master) > 7 {
		master = master[:7]
	}
	if master != "" {
		f += " @" + master
	}
	from = strings.Join(strings.Fields(from), " ")
	if r := []rune(from); len(r) > maxSourceChars {
		from = string(r[:maxSourceChars-1]) + "…"
	}
	if from != "" {
		f += " | relaying " + from
	}
	return f
}
