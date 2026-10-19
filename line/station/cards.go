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
	"time"
	"unicode/utf8"

	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/linestore"
)

const (
	maxTitle    = 200
	maxDesc     = 16 << 10
	maxCardBody = 64 << 10
	viceroyRole = "viceroy"
	makerRole   = "maker"
	// LabelNeedsPrefix starts a label that blocks a card on a role's answer.
	LabelNeedsPrefix = "needs:"
	// needsKing is a valid needs target though the king is not a station role.
	needsKing = "king"
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
// unblocked, unclaimed cards (bd ready) and excludes LabelGaveUp and cards
// deferred now (IsDeferred).
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
	RemoveLabel(ctx context.Context, id, label string) error
	// Claim, Release and Close act as the named agent: bd lets only the
	// claim holder release a claim. Show reports the assignee.
	Claim(ctx context.Context, id, agent string) error
	Release(ctx context.Context, id, agent, note string) error
	Close(ctx context.Context, id, agent, reason string) error
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
	out, err := b.bd(ctx, args...)
	if err != nil || !f.Ready {
		return out, err
	}
	return dropDeferred(out, time.Now())
}

// Show returns `bd show --json` with the card's comments attached: bd show
// omits them (comments_omitted) and reports only comment_count.
func (b BeadsCards) Show(ctx context.Context, id string) (json.RawMessage, error) {
	out, err := b.bd(ctx, "show", id, "--json")
	if err != nil {
		return nil, err
	}
	comments, err := b.bd(ctx, "comments", id, "--json")
	if err != nil {
		return nil, err
	}
	return attachComments(out, comments)
}

// attachComments sets "comments" on the issue in `bd show --json` output (an
// object or a one-element array) and drops the comments_omitted marker.
func attachComments(show, comments json.RawMessage) (json.RawMessage, error) {
	var list []json.RawMessage
	if err := json.Unmarshal(comments, &list); err != nil {
		return nil, fmt.Errorf("bd comments: %w", err)
	}
	var many []map[string]json.RawMessage
	isArray := json.Unmarshal(show, &many) == nil
	if !isArray {
		var one map[string]json.RawMessage
		if err := json.Unmarshal(show, &one); err != nil {
			return nil, fmt.Errorf("bd show: %w", err)
		}
		many = []map[string]json.RawMessage{one}
	}
	for _, issue := range many {
		issue["comments"] = comments
		delete(issue, "comments_omitted")
	}
	if isArray {
		return json.Marshal(many)
	}
	return json.Marshal(many[0])
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

func (b BeadsCards) RemoveLabel(ctx context.Context, id, label string) error {
	_, err := b.bd(ctx, "label", "remove", id, label)
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

func (b BeadsCards) Close(ctx context.Context, id, agent, reason string) error {
	_, err := b.bdAs(ctx, agent, "close", id, "--reason", reason)
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
// call it. Creating a card (POST /v1/cards) is viceroy-only. The viceroy may
// close only cards a constitution delegation covers. Roles other than
// the viceroy may comment and raise needs only on the card they hold.
var cardActions = map[string][]string{
	"comments": {viceroyRole, "maker", "andon", "kaizen", "gemba"},
	"needs":    {"maker", "andon", "kaizen", "gemba"},
	"offer":    {viceroyRole},
	"claim":    {makerRole},
	"close":    {makerRole, viceroyRole},
	"release":  {makerRole},
	"give-up":  {makerRole},
	"labels":   {viceroyRole},
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
	if err := requireMethod(w, r, want); err != nil {
		return "", err
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
	if rest == "" && r.Method == http.MethodGet {
		return s.serveCardList(w, r, cards, st)
	}
	if rest == "" {
		return s.serveCardCreate(w, r, cards)
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
	if parts[1] == "needs" {
		return s.raiseNeed(w, r, cards, st, id)
	}
	if parts[1] == "comments" {
		return s.serveCardComment(w, r, cards, st, id)
	}
	if parts[1] == "labels" {
		return s.viceroyLabels(w, r, cards, st, id)
	}
	if parts[1] == "offer" {
		return s.offer(w, r, cards, st, id)
	}
	if parts[1] == "close" && st.Role == viceroyRole {
		return s.viceroyClose(w, r, cards, st, id)
	}
	return s.settle(w, r, cards, st, id, parts[1])
}

// serveCardList handles GET /v1/cards.
func (s *Server) serveCardList(w http.ResponseWriter, r *http.Request, cards Cards, st *Station) (string, error) {
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
	out, err := cards.List(r.Context(), f)
	if err != nil {
		return "", err
	}
	if f.Ready && st.Role == makerRole {
		if out, err = s.dropBudgetBlocked(r.Context(), out, st.Role); err != nil {
			return "", err
		}
	}
	return "list", writeRaw(w, out)
}

// serveCardCreate handles POST /v1/cards.
func (s *Server) serveCardCreate(w http.ResponseWriter, r *http.Request, cards Cards) (string, error) {
	var c NewCard
	if err := decodeBody(w, r, &c); err != nil {
		return "", err
	}
	if err := validateNew(c); err != nil {
		return "", err
	}
	out, err := cards.Create(r.Context(), c)
	if err != nil {
		return "", err
	}
	return "create " + c.Type, writeRaw(w, out)
}

// serveCardComment handles POST /v1/cards/{id}/comments.
func (s *Server) serveCardComment(w http.ResponseWriter, r *http.Request, cards Cards, st *Station, id string) (string, error) {
	if st.Role != viceroyRole {
		if err := s.admitOwnComment(r, cards, st, id); err != nil {
			return "", err
		}
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		return "", err
	}
	if strings.TrimSpace(body.Text) == "" || len(body.Text) > maxDesc || !utf8.ValidString(body.Text) {
		return "", fail(http.StatusBadRequest, "comment text must be 1..%d bytes of UTF-8", maxDesc)
	}
	if err := cards.Comment(r.Context(), id, body.Text); err != nil {
		return "", err
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return "comment " + id, nil
}

// viceroyClose closes a card the constitution delegates to the viceroy, once
// the card carries a comment recording its decision. The grant is the guard's
// to verify: the viceroy only requests.
func (s *Server) viceroyClose(w http.ResponseWriter, r *http.Request, cards Cards, st *Station, id string) (string, error) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		return "", err
	}
	if err := requireReason(body.Reason); err != nil {
		return "", err
	}
	raw, err := cards.Show(r.Context(), id)
	if err != nil {
		return "", err
	}
	cardType, ancestors := cardLineage(raw, id)
	var labelled struct {
		Labels []string `json:"labels"`
	}
	unmarshalIssue(raw, &labelled)
	if !s.Guard.Constitution.DelegatesClose(id, cardType, labelled.Labels, ancestors) {
		return "", fail(http.StatusForbidden, "no constitution delegation lets the viceroy close %s", id)
	}
	var issue struct {
		Comments []json.RawMessage `json:"comments"`
	}
	if !unmarshalIssue(raw, &issue) || len(issue.Comments) == 0 {
		return "", fail(http.StatusConflict, "card %s has no comment recording its decision", id)
	}
	if err := cards.Close(r.Context(), id, st.AgentID, body.Reason); err != nil {
		return "", err
	}
	if err := s.revokeEpicApproval(id); err != nil {
		return "", err
	}
	s.recordEvent(r.Context(), st, linestore.EventClose, id, body.Reason)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return "close " + id, nil
}

// requireReason rejects a close or release reason that is blank, longer than
// maxDesc bytes or not UTF-8.
func requireReason(reason string) error {
	if strings.TrimSpace(reason) == "" || len(reason) > maxDesc || !utf8.ValidString(reason) {
		return fail(http.StatusBadRequest, "reason must be 1..%d bytes of UTF-8", maxDesc)
	}
	return nil
}

// LabelChange is the body of POST /v1/cards/{id}/labels.
type LabelChange struct {
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
}

// viceroyMayLabel reports whether the viceroy may add or remove label:
// always line-gave-up and needs:*, otherwise what the constitution lists.
func (s *Server) viceroyMayLabel(label string) bool {
	return label == LabelGaveUp || strings.HasPrefix(label, LabelNeedsPrefix) || s.Guard.Constitution.Viceroy.MayLabel(label)
}

// viceroyLabels applies a label change for the viceroy. All labels are
// checked before any is applied; each applied change is its own event whose
// station is the actor.
func (s *Server) viceroyLabels(w http.ResponseWriter, r *http.Request, cards Cards, st *Station, id string) (string, error) {
	var change LabelChange
	if err := decodeBody(w, r, &change); err != nil {
		return "", err
	}
	if len(change.Add)+len(change.Remove) == 0 {
		return "", fail(http.StatusBadRequest, "name at least one label to add or remove")
	}
	for _, label := range slices.Concat(change.Add, change.Remove) {
		if !wordRe.MatchString(label) {
			return "", fail(http.StatusBadRequest, "invalid label %q", label)
		}
		if !s.viceroyMayLabel(label) {
			return "", fail(http.StatusForbidden, "the constitution does not let the viceroy touch label %q", label)
		}
		if slices.Contains(change.Add, label) && slices.Contains(change.Remove, label) {
			return "", fail(http.StatusBadRequest, "label %q is both added and removed", label)
		}
	}
	ctx := r.Context()
	for _, label := range change.Add {
		if err := cards.AddLabel(ctx, id, label); err != nil {
			return "", err
		}
		s.recordEvent(ctx, st, linestore.EventLabel, id, "add "+label)
	}
	for _, label := range change.Remove {
		if err := cards.RemoveLabel(ctx, id, label); err != nil {
			return "", err
		}
		s.recordEvent(ctx, st, linestore.EventLabel, id, "remove "+label)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return "labels " + id, nil
}

// settleEvents is the event each settle action records.
var settleEvents = map[string]linestore.EventKind{
	"claim":   linestore.EventClaim,
	"close":   linestore.EventClose,
	"release": linestore.EventRelease,
	"give-up": linestore.EventGiveUp,
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
		Reason         string `json:"reason"`
		EscalateToKing bool   `json:"escalate_to_king"`
	}
	if action == "give-up" {
		if err := decodeBody(w, r, &body); err != nil {
			return "", err
		}
	}
	if action == "close" || action == "release" {
		if err := decodeBody(w, r, &body); err != nil {
			return "", err
		}
		if err := requireReason(body.Reason); err != nil {
			return "", err
		}
	}
	var err error
	switch action {
	case "claim":
		reason, berr := s.budgetBlockFor(ctx, st.Role, id)
		if berr != nil {
			return "", berr
		}
		if reason != "" {
			return "", fail(http.StatusTooManyRequests, "%s", reason)
		}
		err = cards.Claim(ctx, id, st.AgentID)
		if err == nil {
			s.commentMu.Lock()
			delete(s.commentCounts, commentKey{st.AgentID, id})
			s.commentMu.Unlock()
		}
	case "close":
		err = cards.Close(ctx, id, st.AgentID, body.Reason)
		if err == nil {
			err = s.revokeEpicApproval(id)
		}
	case "release":
		err = cards.Release(ctx, id, st.AgentID, body.Reason)
	case "give-up":
		err = cards.AddLabel(ctx, id, LabelGaveUp)
		if err == nil && body.EscalateToKing {
			err = cards.AddLabel(ctx, id, LabelNeedsPrefix+needsKing)
		}
	}
	if err != nil {
		return "", err
	}
	s.recordEvent(ctx, st, settleEvents[action], id, body.Reason)
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
// it to the king. It is idempotent: a card already on the line (offered, or
// accepted by the king) answers Offered with the line label and is left
// untouched; a card already proposed is not labelled twice.
func (s *Server) offer(w http.ResponseWriter, r *http.Request, cards Cards, st *Station, id string) (string, error) {
	raw, err := cards.Show(r.Context(), id)
	if err != nil {
		return "", err
	}
	var issue struct {
		Labels []string `json:"labels"`
	}
	unmarshalIssue(raw, &issue)
	if slices.Contains(issue.Labels, LabelLine) {
		res := Offer{Offered: true, Label: LabelLine}
		writeJSON(w, http.StatusOK, res)
		return fmt.Sprintf("offer %s label=%s (already)", id, res.Label), nil
	}
	typ, ancestors := cardLineage(raw, id)
	res := Offer{Label: LabelProposed}
	inherited, err := s.underApprovedEpic(r.Context(), cards, ancestors)
	if err != nil {
		return "", err
	}
	if s.Guard.Constitution.Delegates(typ, ancestors, inherited) {
		res = Offer{Offered: true, Label: LabelLine}
	}
	if !slices.Contains(issue.Labels, res.Label) {
		if err := cards.AddLabel(r.Context(), id, res.Label); err != nil {
			return "", err
		}
	}
	s.recordEvent(r.Context(), st, linestore.EventOffer, id, "label "+res.Label)
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

// commentKey identifies one agent's run on one card; claiming resets it.
type commentKey struct{ agentID, cardID string }

// requireOwnCard fails with 403 unless the station's agent holds the card.
func requireOwnCard(r *http.Request, cards Cards, st *Station, id string) error {
	raw, err := cards.Show(r.Context(), id)
	if err != nil {
		return err
	}
	if holder := cardAssignee(raw); holder != st.AgentID {
		return fail(http.StatusForbidden, "card %s is claimed by %q, not %q", id, holder, st.AgentID)
	}
	return nil
}

// admitOwnComment lets a non-viceroy role comment on its own card, at most
// the constitution's cards.comment_cap times per run (claim to claim).
func (s *Server) admitOwnComment(r *http.Request, cards Cards, st *Station, id string) error {
	if err := requireOwnCard(r, cards, st, id); err != nil {
		return err
	}
	key := commentKey{st.AgentID, id}
	s.commentMu.Lock()
	defer s.commentMu.Unlock()
	if n := s.commentCounts[key]; n >= s.Guard.Constitution.Cards.CommentCapOrDefault() {
		return fail(http.StatusTooManyRequests, "comment cap of %d reached for card %s", n, id)
	}
	if s.commentCounts == nil {
		s.commentCounts = map[commentKey]int{}
	}
	s.commentCounts[key]++
	return nil
}

// raiseNeed labels the caller's own card needs:<role>, blocking it on that
// role. The recipient may decline; the agent never waits.
func (s *Server) raiseNeed(w http.ResponseWriter, r *http.Request, cards Cards, st *Station, id string) (string, error) {
	var body struct {
		Role string `json:"role"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		return "", err
	}
	if body.Role != needsKing && !slices.Contains(constitution.Roles, body.Role) {
		return "", fail(http.StatusBadRequest, "role %q: want king or one of %s", body.Role, strings.Join(constitution.Roles, ", "))
	}
	if err := requireOwnCard(r, cards, st, id); err != nil {
		return "", err
	}
	if err := cards.AddLabel(r.Context(), id, LabelNeedsPrefix+body.Role); err != nil {
		return "", err
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return "needs " + id + " " + body.Role, nil
}
