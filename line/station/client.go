package station

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/witness"
)

// Client talks to the station API from inside a station container.
type Client struct {
	URL   string // e.g. http://host.containers.internal:7468
	Token string
	Gen   string       // viceroy generation, sent as X-Sweatshop-Gen
	HTTP  *http.Client // nil → default with 3h timeout (checks are slow)
}

func (c Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 3 * time.Hour}
}

// Busy-retry policy: the line runs one check at a time and answers 409 busy
// while another is in flight.
var (
	busyInitial = 5 * time.Second
	busyMax     = 60 * time.Second
	busyGiveUp  = 30 * time.Minute
)

// do sends a request, retrying 409 busy with capped backoff, and returns the
// result of the last attempt.
func (c Client) do(ctx context.Context, method, path string, query url.Values, body io.Reader, out any) ([]byte, error) {
	deadline := time.Now().Add(busyGiveUp)
	wait := busyInitial
	for {
		data, busy, err := c.doOnce(ctx, method, path, query, body, out)
		if !busy {
			return data, err
		}
		if time.Now().Add(wait).After(deadline) {
			return nil, fmt.Errorf("%w (gave up waiting after %s)", err, busyGiveUp)
		}
		seeker, ok := body.(io.Seeker)
		if body != nil && !ok {
			return nil, err
		}
		if ok {
			if _, serr := seeker.Seek(0, io.SeekStart); serr != nil {
				return nil, err
			}
		}
		fmt.Fprintf(os.Stderr, "station %s %s: line busy, retrying in %s\n", method, path, wait)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
		wait = min(wait*2, busyMax)
	}
}

// doOnce is one attempt; busy reports a 409 {"error":"busy"} response. It
// decodes a 2xx JSON body into out (or returns the raw body when out is nil). Non-2xx responses become errors carrying the
// server's message.
func (c Client) doOnce(ctx context.Context, method, path string, query url.Values, body io.Reader, out any) (_ []byte, busy bool, _ error) {
	u := strings.TrimRight(c.URL, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	if f, ok := body.(*os.File); ok {
		body = io.NopCloser(f) // the transport must not close a body we may resend
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if c.Gen != "" {
		req.Header.Set(GenHeader, c.Gen)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return nil, resp.StatusCode == http.StatusConflict && e.Error == "busy", fmt.Errorf("station %s %s: %s: %s", method, path, resp.Status, e.Error)
		}
		return nil, false, fmt.Errorf("station %s %s: %s", method, path, resp.Status)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return nil, false, fmt.Errorf("station %s %s: decode response: %w", method, path, err)
		}
	}
	return data, false, nil
}

// bundle resolves rev in repoDir and writes a bundle carrying exactly one ref
// under refs/sweatshop/ (the witness's contract), excluding what the line
// already has. The caller must call cleanup.
func bundle(ctx context.Context, repoDir, rev string) (commit, path string, cleanup func(), err error) {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return "", "", nil, fmt.Errorf("invalid revision %q", rev)
	}
	repo := gitx.Repo{Dir: repoDir}
	if commit, err = repo.Commit(ctx, rev); err != nil {
		return "", "", nil, fmt.Errorf("resolve %s: %w", rev, err)
	}
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", "", nil, err
	}
	ref := "refs/sweatshop/bundle-" + hex.EncodeToString(raw)
	if _, err := repo.Git(ctx, "update-ref", "--end-of-options", ref, commit); err != nil {
		return "", "", nil, err
	}
	defer func() { _ = repo.DeleteRef(context.WithoutCancel(ctx), ref) }()

	dir, err := os.MkdirTemp("", "sweatshop-bundle-")
	if err != nil {
		return "", "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	path = filepath.Join(dir, "candidate.bundle")
	args := []string{"bundle", "create", "--quiet", path, ref}
	const base = "refs/remotes/origin/master"
	if exists, err := repo.RefExists(ctx, base); err != nil {
		cleanup()
		return "", "", nil, err
	} else if exists {
		args = append(args, "^"+base)
	}
	if _, err := repo.Git(ctx, args...); err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("bundle %s (is it already on origin/master?): %w", rev, err)
	}
	return commit, path, cleanup, nil
}

func (c Client) postBundle(ctx context.Context, path, repoDir, rev string, out any) error {
	commit, file, cleanup, err := bundle(ctx, repoDir, rev)
	if err != nil {
		return err
	}
	defer cleanup()
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = c.do(ctx, http.MethodPost, path, url.Values{"commit": {commit}}, f, out)
	return err
}

// Attest asks the station's witness to run the required checks on rev.
func (c Client) Attest(ctx context.Context, repoDir, rev string) (witness.AttestResult, error) {
	var res witness.AttestResult
	err := c.postBundle(ctx, "/v1/attest", repoDir, rev, &res)
	return res, err
}

// Merge attests rev and, if it passes, submits it to the merge guard.
func (c Client) Merge(ctx context.Context, repoDir, rev string) (witness.MergeResult, error) {
	var res witness.MergeResult
	err := c.postBundle(ctx, "/v1/merge", repoDir, rev, &res)
	return res, err
}

// AttestLine attests a commit already on the line (the andon watcher's call).
func (c Client) AttestLine(ctx context.Context, commit string) (witness.AttestResult, error) {
	var res witness.AttestResult
	_, err := c.do(ctx, http.MethodPost, "/v1/attest-line", url.Values{"commit": {commit}}, nil, &res)
	return res, err
}

func (c Client) andon(ctx context.Context, path string, env attest.Envelope) (guard.Result, error) {
	body, err := json.Marshal(env)
	if err != nil {
		return guard.Result{}, err
	}
	var res guard.Result
	_, err = c.do(ctx, http.MethodPost, path, nil, bytes.NewReader(body), &res)
	return res, err
}

// Pull stops the line on a signed failing attestation of the current master.
func (c Client) Pull(ctx context.Context, env attest.Envelope) (guard.Result, error) {
	return c.andon(ctx, "/v1/andon/pull", env)
}

// Clear restarts the line on a signed passing attestation of the current master.
func (c Client) Clear(ctx context.Context, env attest.Envelope) (guard.Result, error) {
	return c.andon(ctx, "/v1/andon/clear", env)
}

func (c Client) Status(ctx context.Context) (Status, error) {
	var st Status
	_, err := c.do(ctx, http.MethodGet, "/v1/status", nil, nil, &st)
	return st, err
}

// Heartbeat tells the line this station is alive and what it is doing.
func (c Client) Heartbeat(ctx context.Context, hb Heartbeat) error {
	_, err := c.postJSON(ctx, "/v1/stations/heartbeat", hb)
	return err
}

// Evidence returns the check log recorded under id.
func (c Client) Evidence(ctx context.Context, id string) ([]byte, error) {
	return c.do(ctx, http.MethodGet, "/v1/evidence/"+url.PathEscape(id), nil, nil, nil)
}

// CardsList lists cards as the host's bd reports them (JSON).
func (c Client) CardsList(ctx context.Context, f ListFilter) ([]byte, error) {
	q := url.Values{}
	for k, v := range map[string]string{"status": f.Status, "label": f.Label, "parent": f.Parent} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if f.Ready {
		q.Set("ready", "true")
	}
	return c.do(ctx, http.MethodGet, "/v1/cards", q, nil, nil)
}

func (c Client) CardShow(ctx context.Context, id string) ([]byte, error) {
	return c.do(ctx, http.MethodGet, "/v1/cards/"+url.PathEscape(id), nil, nil, nil)
}

func (c Client) postJSON(ctx context.Context, path string, v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, http.MethodPost, path, nil, bytes.NewReader(body), nil)
}

// CardCreate creates a card (viceroy only).
func (c Client) CardCreate(ctx context.Context, nc NewCard) ([]byte, error) {
	return c.postJSON(ctx, "/v1/cards", nc)
}

// CardComment adds a comment (viceroy or maker).
func (c Client) CardComment(ctx context.Context, id, text string) ([]byte, error) {
	return c.postJSON(ctx, "/v1/cards/"+url.PathEscape(id)+"/comments", map[string]string{"text": text})
}

func (c Client) cardAction(ctx context.Context, id, action string, body any) error {
	_, err := c.postJSON(ctx, "/v1/cards/"+url.PathEscape(id)+"/"+action, body)
	return err
}

// CardClaim claims the card for the calling station's agent (maker only).
func (c Client) CardClaim(ctx context.Context, id string) error {
	return c.cardAction(ctx, id, "claim", struct{}{})
}

// CardClose closes a card the station holds (maker only).
func (c Client) CardClose(ctx context.Context, id, reason string) error {
	return c.cardAction(ctx, id, "close", map[string]string{"reason": reason})
}

// CardRelease reopens a card the station holds, recording reason (maker only).
func (c Client) CardRelease(ctx context.Context, id, reason string) error {
	return c.cardAction(ctx, id, "release", map[string]string{"reason": reason})
}

// CardGiveUp labels a card the station holds LabelGaveUp (maker only).
func (c Client) CardGiveUp(ctx context.Context, id string) error {
	return c.cardAction(ctx, id, "give-up", struct{}{})
}

// CardOffer labels the card for the line or proposes it (viceroy only).
func (c Client) CardOffer(ctx context.Context, id string) ([]byte, error) {
	return c.do(ctx, http.MethodPost, "/v1/cards/"+url.PathEscape(id)+"/offer", nil, nil, nil)
}

// ChannelInbox returns the channel messages after the given ID as JSON,
// waiting up to wait for one (viceroy only).
func (c Client) ChannelInbox(ctx context.Context, after string, wait time.Duration) ([]byte, error) {
	q := url.Values{}
	if after != "" {
		q.Set("after", after)
	}
	if wait > 0 {
		q.Set("wait", wait.String())
	}
	return c.do(ctx, http.MethodGet, "/v1/channel/inbox", q, nil, nil)
}

// ChannelPost posts one message of at most 1900 characters (viceroy only).
//
// from names whose message is being relayed ("" for the viceroy's own words);
// the station appends the speaker footer.
func (c Client) ChannelPost(ctx context.Context, text, from string) error {
	_, err := c.postJSON(ctx, "/v1/channel/post", map[string]string{"text": text, "from": from})
	return err
}

// ChannelTyping shows the bot as typing in the channel for about ten seconds
// (viceroy only).
func (c Client) ChannelTyping(ctx context.Context) error {
	_, err := c.postJSON(ctx, "/v1/channel/typing", struct{}{})
	return err
}

// TestamentGet returns the viceroy's latest testament; ok is false when none
// has been stored.
func (c Client) TestamentGet(ctx context.Context) (body []byte, ok bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+"/v1/testament", nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, err
	}
	switch {
	case resp.StatusCode == http.StatusNoContent:
		return nil, false, nil
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return nil, false, fmt.Errorf("station GET /v1/testament: %s: %s", resp.Status, e.Error)
		}
		return nil, false, fmt.Errorf("station GET /v1/testament: %s", resp.Status)
	}
	return data, true, nil
}

// TestamentPut stores a new testament version (viceroy only).
func (c Client) TestamentPut(ctx context.Context, body []byte) error {
	_, err := c.do(ctx, http.MethodPut, "/v1/testament", nil, bytes.NewReader(body), nil)
	return err
}

// ConstitutionGet returns the current constitution TOML.
func (c Client) ConstitutionGet(ctx context.Context) ([]byte, error) {
	return c.do(ctx, http.MethodGet, "/v1/constitution", nil, nil, nil)
}

// ConstitutionEntry is one commit in the constitution's history.
type ConstitutionEntry struct {
	Commit  string `json:"commit"`
	Message string `json:"message"`
}

// ConstitutionLog lists the constitution's history, newest first.
func (c Client) ConstitutionLog(ctx context.Context) ([]ConstitutionEntry, error) {
	var out struct {
		Entries []ConstitutionEntry `json:"entries"`
	}
	_, err := c.do(ctx, http.MethodGet, "/v1/constitution/log", nil, nil, &out)
	return out.Entries, err
}

// ConstitutionPropose submits a full new constitution for the king's approval
// (viceroy only) and returns the proposal number.
func (c Client) ConstitutionPropose(ctx context.Context, toml []byte) (int, error) {
	var out struct {
		Proposal int `json:"proposal"`
	}
	_, err := c.do(ctx, http.MethodPost, "/v1/constitution/proposals", nil, bytes.NewReader(toml), &out)
	return out.Proposal, err
}

// ViceroySucceed asks the line to start a new viceroy generation at commit
// (viceroy only).
func (c Client) ViceroySucceed(ctx context.Context, commit string) ([]byte, error) {
	return c.do(ctx, http.MethodPost, "/v1/viceroy/succeed", url.Values{"commit": {commit}}, nil, nil)
}

// ViceroyCheckin reports that this generation is up (viceroy only).
func (c Client) ViceroyCheckin(ctx context.Context) ([]byte, error) {
	return c.do(ctx, http.MethodPost, "/v1/viceroy/checkin", nil, nil, nil)
}

// DeployRequest asks the line to redeploy itself to commit, which the king
// must then approve (viceroy only).
func (c Client) DeployRequest(ctx context.Context, commit string) ([]byte, error) {
	return c.do(ctx, http.MethodPost, "/v1/deploy/requests", url.Values{"commit": {commit}}, nil, nil)
}
