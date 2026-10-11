package station

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxTitle    = 200
	maxDesc     = 16 << 10
	maxCardBody = 64 << 10
	viceroyRole = "viceroy"
	makerRole   = "maker"
	// LabelLine puts a card on the line; LabelProposed asks the king to.
	LabelLine     = "line"
	LabelProposed = "line-proposed"
	// LabelGaveUp marks a card a maker failed to land repeatedly. An operator
	// removes it (`bd label remove <id> line-gave-up`) to retry.
	LabelGaveUp = "line-gave-up"
)

var (
	cardIDRe  = regexp.MustCompile(`^[a-z0-9-]+(\.[0-9]+)*$`)
	wordRe    = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	cardTypes = []string{"bug", "feature", "task", "epic", "chore"}
)

// ListFilter selects cards; empty fields do not filter. Ready lists only
// unblocked, unclaimed cards (bd ready) and excludes LabelGaveUp.
type ListFilter struct {
	Status, Label, Parent string
	Ready                 bool
}

// NewCard is the body of POST /v1/cards.
type NewCard struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type"`
	Priority    int    `json:"priority"`
	Parent      string `json:"parent,omitempty"`
	Acceptance  string `json:"acceptance,omitempty"`
}

// Cards is the line's card tracker. The production implementation runs bd on
// the host; tests use a fake.
type Cards interface {
	List(ctx context.Context, f ListFilter) (json.RawMessage, error)
	Show(ctx context.Context, id string) (json.RawMessage, error)
	Create(ctx context.Context, c NewCard) (json.RawMessage, error)
	Comment(ctx context.Context, id, text string) error
	AddLabel(ctx context.Context, id, label string) error
	// Claim, Release and Close act as the named agent: bd lets only the
	// claim holder release a claim. Show reports the assignee.
	Claim(ctx context.Context, id, agent string) error
	Release(ctx context.Context, id, agent, note string) error
	Close(ctx context.Context, id, reason string) error
}

// BeadsCards runs bd in Dir with fixed argv (no shell).
type BeadsCards struct{ Dir string }

func (b BeadsCards) bd(ctx context.Context, args ...string) ([]byte, error) {
	return b.bdAs(ctx, "", args...)
}

func (b BeadsCards) bdAs(ctx context.Context, actor string, args ...string) ([]byte, error) {
	if actor != "" {
		args = append(args, "--actor", actor)
	}
	cmd := exec.CommandContext(ctx, "bd", args...)
	cmd.Dir = b.Dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bd %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (b BeadsCards) List(ctx context.Context, f ListFilter) (json.RawMessage, error) {
	args := []string{"list", "--limit", "0", "--json"}
	if f.Ready {
		args = []string{"ready", "--exclude-label", LabelGaveUp, "--limit", "0", "--json"}
	}
	if f.Status != "" {
		args = append(args, "--status", f.Status)
	}
	if f.Label != "" {
		args = append(args, "--label", f.Label)
	}
	if f.Parent != "" {
		args = append(args, "--parent", f.Parent)
	}
	return b.bd(ctx, args...)
}

func (b BeadsCards) Show(ctx context.Context, id string) (json.RawMessage, error) {
	return b.bd(ctx, "show", id, "--json")
}

func (b BeadsCards) Create(ctx context.Context, c NewCard) (json.RawMessage, error) {
	args := []string{"create", "--title=" + c.Title, "--type=" + c.Type, "--priority=" + strconv.Itoa(c.Priority)}
	if c.Description != "" {
		args = append(args, "--description="+c.Description)
	}
	if c.Parent != "" {
		args = append(args, "--parent="+c.Parent)
	}
	if c.Acceptance != "" {
		args = append(args, "--acceptance="+c.Acceptance)
	}
	return b.bd(ctx, append(args, "--json")...)
}

func (b BeadsCards) Comment(ctx context.Context, id, text string) error {
	_, err := b.bd(ctx, "comments", "add", "--", id, text)
	return err
}

func (b BeadsCards) AddLabel(ctx context.Context, id, label string) error {
	_, err := b.bd(ctx, "label", "add", id, label)
	return err
}

func (b BeadsCards) Claim(ctx context.Context, id, agent string) error {
	_, err := b.bdAs(ctx, agent, "update", id, "--claim")
	return err
}

// Release reopens the card, clearing the assignee, and records note as a
// comment (bd unclaim --reason).
func (b BeadsCards) Release(ctx context.Context, id, agent, note string) error {
	_, err := b.bdAs(ctx, agent, "unclaim", id, "--reason", note)
	return err
}

func (b BeadsCards) Close(ctx context.Context, id, reason string) error {
	_, err := b.bd(ctx, "close", id, "--reason", reason)
	return err
}

func validID(id string) error {
	if !cardIDRe.MatchString(id) {
		return fail(http.StatusBadRequest, "invalid card id %q", id)
	}
	return nil
}

func (s *Server) cards() (Cards, error) {
	if s.Cards == nil {
		return nil, fail(http.StatusServiceUnavailable, "cards are not configured (line serve --beads)")
	}
	return s.Cards, nil
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCardBody)).Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return fail(http.StatusRequestEntityTooLarge, "body exceeds %d bytes", maxCardBody)
		}
		return fail(http.StatusBadRequest, "decode body: %v", err)
	}
	return nil
}

// cardActions maps each POST /v1/cards/{id}/{action} to the roles allowed to
// call it. Creating a card (POST /v1/cards) is viceroy-only.
var cardActions = map[string][]string{
	"comments": {viceroyRole},
	"offer":    {viceroyRole},
	"claim":    {makerRole},
	"close":    {makerRole},
	"release":  {makerRole},
	"give-up":  {makerRole},
}

// routeCards handles /v1/cards and /v1/cards/{id}[/<action in cardActions>].
func (s *Server) routeCards(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/cards")
	rest = strings.TrimPrefix(rest, "/")
	parts := strings.Split(rest, "/")
	var want string
	switch {
	case rest == "":
		want = r.Method
		if want != http.MethodGet && want != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			return "", fail(http.StatusMethodNotAllowed, "use GET or POST for %s", r.URL.Path)
		}
	case len(parts) == 1:
		want = http.MethodGet
	case len(parts) == 2 && cardActions[parts[1]] != nil:
		want = http.MethodPost
	default:
		return "", fail(http.StatusNotFound, "no route %s", r.URL.Path)
	}
	if r.Method != want {
		w.Header().Set("Allow", want)
		return "", fail(http.StatusMethodNotAllowed, "use %s for %s", want, r.URL.Path)
	}
	if r.Method == http.MethodPost {
		roles := []string{viceroyRole}
		if rest != "" {
			roles = cardActions[parts[1]]
		}
		if !slices.Contains(roles, st.Role) {
			return "", fail(http.StatusForbidden, "role %q may not write cards", st.Role)
		}
		if st.Role == viceroyRole && s.Viceroy != nil {
			if err := s.Viceroy.Admit(r, false); err != nil {
				return "", err
			}
		}
	}
	cards, err := s.cards()
	if err != nil {
		return "", err
	}
	ctx := r.Context()
	switch {
	case rest == "" && r.Method == http.MethodGet:
		q := r.URL.Query()
		f := ListFilter{Status: q.Get("status"), Label: q.Get("label"), Parent: q.Get("parent"), Ready: q.Get("ready") == "true"}
		for _, v := range []string{f.Status, f.Label} {
			if v != "" && !wordRe.MatchString(v) {
				return "", fail(http.StatusBadRequest, "invalid filter %q", v)
			}
		}
		if f.Parent != "" {
			if err := validID(f.Parent); err != nil {
				return "", err
			}
		}
		out, err := cards.List(ctx, f)
		if err != nil {
			return "", err
		}
		return "list", writeRaw(w, out)
	case rest == "":
		var c NewCard
		if err := decodeBody(w, r, &c); err != nil {
			return "", err
		}
		if err := validateNew(c); err != nil {
			return "", err
		}
		out, err := cards.Create(ctx, c)
		if err != nil {
			return "", err
		}
		return "create " + c.Type, writeRaw(w, out)
	}
	id := parts[0]
	if err := validID(id); err != nil {
		return "", err
	}
	if len(parts) == 1 {
		out, err := cards.Show(ctx, id)
		if err != nil {
			return "", err
		}
		return "show " + id, writeRaw(w, out)
	}
	if parts[1] == "comments" {
		var body struct {
			Text string `json:"text"`
		}
		if err := decodeBody(w, r, &body); err != nil {
			return "", err
		}
		if strings.TrimSpace(body.Text) == "" || len(body.Text) > maxDesc || !utf8.ValidString(body.Text) {
			return "", fail(http.StatusBadRequest, "comment text must be 1..%d bytes of UTF-8", maxDesc)
		}
		if err := cards.Comment(ctx, id, body.Text); err != nil {
			return "", err
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return "comment " + id, nil
	}
	if parts[1] == "offer" {
		return s.offer(w, r, cards, id)
	}
	return s.settle(w, r, cards, st, id, parts[1])
}

// settle runs a maker's claim, close, release or give-up. The caller must
// hold the claim (give-up also runs after release, so an unclaimed card
// passes): the station container holds the same maker token as its runner,
// and must not touch other cards.
func (s *Server) settle(w http.ResponseWriter, r *http.Request, cards Cards, st *Station, id, action string) (string, error) {
	ctx := r.Context()
	if action != "claim" {
		raw, err := cards.Show(ctx, id)
		if err != nil {
			return "", err
		}
		holder := cardAssignee(raw)
		if holder != st.AgentID && !(action == "give-up" && holder == "") {
			return "", fail(http.StatusForbidden, "card %s is claimed by %q, not %q", id, holder, st.AgentID)
		}
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if action == "close" || action == "release" {
		if err := decodeBody(w, r, &body); err != nil {
			return "", err
		}
		if strings.TrimSpace(body.Reason) == "" || len(body.Reason) > maxDesc || !utf8.ValidString(body.Reason) {
			return "", fail(http.StatusBadRequest, "reason must be 1..%d bytes of UTF-8", maxDesc)
		}
	}
	var err error
	switch action {
	case "claim":
		err = cards.Claim(ctx, id, st.AgentID)
	case "close":
		err = cards.Close(ctx, id, body.Reason)
	case "release":
		err = cards.Release(ctx, id, st.AgentID, body.Reason)
	case "give-up":
		err = cards.AddLabel(ctx, id, LabelGaveUp)
	}
	if err != nil {
		return "", err
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return action + " " + id, nil
}

func writeRaw(w http.ResponseWriter, out json.RawMessage) error {
	if !json.Valid(out) {
		return errors.New("bd returned invalid JSON")
	}
	w.Header().Set("Content-Type", "application/json")
	_, err := w.Write(out)
	return err
}

func validateNew(c NewCard) error {
	switch {
	case strings.TrimSpace(c.Title) == "" || len(c.Title) > maxTitle || !utf8.ValidString(c.Title):
		return fail(http.StatusBadRequest, "title must be 1..%d bytes", maxTitle)
	case len(c.Description) > maxDesc || len(c.Acceptance) > maxDesc:
		return fail(http.StatusBadRequest, "description and acceptance are capped at %d bytes", maxDesc)
	case !slices.Contains(cardTypes, c.Type):
		return fail(http.StatusBadRequest, "type %q: want one of %s", c.Type, strings.Join(cardTypes, ", "))
	case c.Priority < 0 || c.Priority > 4:
		return fail(http.StatusBadRequest, "priority %d: want 0..4", c.Priority)
	}
	if c.Parent != "" {
		return validID(c.Parent)
	}
	return nil
}

// Offer is the response of POST /v1/cards/{id}/offer.
type Offer struct {
	Offered bool   `json:"offered"`
	Label   string `json:"label"`
}

// offer puts the card on the line when a delegation covers it, else proposes
// it to the king.
func (s *Server) offer(w http.ResponseWriter, r *http.Request, cards Cards, id string) (string, error) {
	raw, err := cards.Show(r.Context(), id)
	if err != nil {
		return "", err
	}
	typ, ancestors := cardLineage(raw, id)
	res := Offer{Label: LabelProposed}
	if s.Guard.Constitution.Delegates(typ, ancestors) {
		res = Offer{Offered: true, Label: LabelLine}
	}
	if err := cards.AddLabel(r.Context(), id, res.Label); err != nil {
		return "", err
	}
	writeJSON(w, http.StatusOK, res)
	return fmt.Sprintf("offer %s label=%s", id, res.Label), nil
}

// cardLineage reads the issue type and ancestor IDs from `bd show --json`
// output. Ancestors come from the
// hierarchical ID (a.1.2 sits under a.1 and a) and an explicit parent field.
func cardLineage(raw json.RawMessage, id string) (string, []string) {
	var issue struct {
		Type   string `json:"issue_type"`
		Parent string `json:"parent"`
	}
	if !unmarshalIssue(raw, &issue) {
		return "", nil
	}
	var anc []string
	for p := id; ; {
		i := strings.LastIndex(p, ".")
		if i < 0 {
			break
		}
		p = p[:i]
		anc = append(anc, p)
	}
	if issue.Parent != "" && !slices.Contains(anc, issue.Parent) {
		anc = append(anc, issue.Parent)
	}
	return issue.Type, anc
}

// unmarshalIssue decodes `bd show --json` output (an object or a one-element
// array) into v. It reports false when the array is empty.
func unmarshalIssue(raw json.RawMessage, v any) bool {
	var many []json.RawMessage
	if json.Unmarshal(raw, &many) == nil {
		if len(many) == 0 {
			return false
		}
		raw = many[0]
	}
	_ = json.Unmarshal(raw, v)
	return true
}

// cardAssignee returns who holds the card's claim ("" when unclaimed).
func cardAssignee(raw json.RawMessage) string {
	var issue struct {
		Assignee string `json:"assignee"`
	}
	unmarshalIssue(raw, &issue)
	return issue.Assignee
}
