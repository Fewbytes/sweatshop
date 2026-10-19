// Package station is the station API: the only way containerized agents reach
// the trusted line (witness keys, merge guard, line remote) on the host. Each
// station's bearer token selects its witness, which fixes the role, agent ID
// and signing key, so a stolen token is only that agent's own identity.
package station

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/linedir"
	"github.com/Fewbytes/sweatshop/line/linestore"
	"github.com/Fewbytes/sweatshop/line/report"
	"github.com/Fewbytes/sweatshop/line/witness"
)

const (
	defaultMaxBundle = 256 << 20
	// maxEnvelope bounds andon request bodies; an envelope is a few KiB.
	maxEnvelope = 1 << 20
	// rejectedObjectGrace keeps a rejected candidate's objects only long
	// enough for a concurrently pushed candidate to get its ref.
	rejectedObjectGrace = time.Minute
)

type Station struct {
	Name, Role, AgentID string
	Token               string
	Witness             *witness.Witness
	// StateDir receives bundle uploads while they are attested; "" means the
	// system temp directory.
	StateDir string
}

const (
	// successionSlotWait bounds how long a viceroy succession waits for the check slot.
	successionSlotWait = 10 * time.Minute
	slotPollEvery      = 50 * time.Millisecond
)

// errBusy is returned while the line already has a check run in flight.
var errBusy = fail(http.StatusConflict, "busy")

type Server struct {
	Stations    []*Station
	Guard       guard.Guard
	EvidenceDir func(st *Station) string // where LogRunner wrote evidence for that station
	MaxBundle   int64                    // default 256 << 20
	Cards       Cards                    // nil: the cards routes return 503
	Channel     *Channel                 // nil: the channel routes return 503
	Viceroy     *Supervisor              // nil: the viceroy routes return 503, no generation guard
	Reports     report.ObjectStore       // object store for kaizen analysis reports; nil: that route returns 503
	Store       *linestore.Store         // the line database (see OpenStore); nil: the runs, stats, kaizen signals and heijunka levels routes return 503
	Deploy      *DeployState             // nil: the deploy routes return 503
	Epics       *EpicApprovals           // nil: no epic is approved, so nothing inherits
	DeployToken string                   // the actuator's bearer token; "": no actuator
	Releases    ReleaseBuilder           // nil: releases are commits only

	// busy admits one check-running request (attest, merge, attest-line) at a
	// time across all stations, so several tokens cannot run checks in
	// parallel to exhaust the host.
	busy atomic.Bool

	// queue admits one waiter at a time into the polling for busy; Go channel
	// senders are served first come, first served.
	queue     chan struct{}
	queueOnce sync.Once

	liveness liveness

	// reporter is shared by every station's witness; OpenStore points it at the database.
	reporter *report.Reporter

	// kaizenRequest is the latest request for a Kaizen run, in memory: after
	// a restart of line serve Heijunka simply asks again.
	kaizenMu      sync.Mutex
	kaizenRequest *KaizenRequest

	// budgetMu serialises usage reports so each sees the usage before it and
	// announces a threshold crossing once.
	budgetMu sync.Mutex

	// commentCounts tallies comments non-viceroy agents made this run.
	commentMu     sync.Mutex
	commentCounts map[commentKey]int
}

// acquire claims the line's single check slot; release with s.busy.Store(false).
func (s *Server) acquire() bool { return s.busy.CompareAndSwap(false, true) }

// acquireQueued waits up to limit for the check slot, serving concurrent
// callers in arrival order. It reports errBusy when the limit passes and the
// context's error when the caller goes away first; release as for acquire.
func (s *Server) acquireQueued(ctx context.Context, limit time.Duration) error {
	s.queueOnce.Do(func() { s.queue = make(chan struct{}, 1) })
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	select {
	case s.queue <- struct{}{}:
	case <-ctx.Done():
		return queueWaitError(ctx)
	}
	defer func() { <-s.queue }()
	for !s.acquire() {
		select {
		case <-ctx.Done():
			return queueWaitError(ctx)
		case <-time.After(slotPollEvery):
		}
	}
	return nil
}

func queueWaitError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errBusy
	}
	return ctx.Err()
}

type Status struct {
	Master string            `json:"master"`
	Event  string            `json:"event,omitempty"`
	Andon  *guard.AndonEvent `json:"andon,omitempty"`
	// Stations is every maker's liveness, plus any other station that has sent
	// a heartbeat.
	Stations []StationStatus `json:"stations"`
	// KaizenRequest is the latest request for a Kaizen run; nil if none was made.
	KaizenRequest *KaizenRequest `json:"kaizen_request,omitempty"`
	// Held is every open line card that makers are not offered, with why.
	Held []HeldCard `json:"held,omitempty"`
	// Budgets is the spend against each constitution token budget.
	Budgets []BudgetStatus `json:"budgets,omitempty"`
}

// HeldCard is an open line card hidden from makers by Reason.
type HeldCard struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// heldCards lists open line cards carrying LabelGaveUp or deferred now, which
// makers are not offered. It returns nil when the server has no card tracker.
func (s *Server) heldCards(ctx context.Context) ([]HeldCard, error) {
	if s.Cards == nil {
		return nil, nil
	}
	out, err := s.Cards.List(ctx, ListFilter{Status: "open", Label: LabelLine})
	if err != nil {
		return nil, fmt.Errorf("list open line cards: %w", err)
	}
	var rows []struct {
		ID     string   `json:"id"`
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse open line cards: %w", err)
	}
	now := time.Now()
	var held []HeldCard
	for _, r := range rows {
		if slices.Contains(r.Labels, LabelGaveUp) {
			held = append(held, HeldCard{ID: r.ID, Reason: "labelled " + LabelGaveUp + "; remove it to offer the card to makers"})
		}
		if IsDeferred(r.Labels, now) {
			held = append(held, HeldCard{ID: r.ID, Reason: "deferred; the king's approve or its " + LabelDeferUntilPrefix + " date brings it back"})
		}
	}
	return held, nil
}

// NewFromLine builds the server for a line directory: one witness per
// station, all pushing to and guarded on the line's remote.
func NewFromLine(l linedir.Line) (*Server, error) {
	repo := gitx.Repo{Dir: l.Remote}
	c, digest, err := LoadConstitution(context.Background(), repo, l.Constitution)
	if err != nil {
		return nil, err
	}
	s := &Server{
		Guard: guard.Guard{Repo: repo, Ref: "refs/heads/master", Constitution: c, ConstitutionDigest: digest, RejectedObjectGrace: rejectedObjectGrace},
		EvidenceDir: func(st *Station) string {
			return filepath.Join(st.StateDir, "evidence")
		},
	}
	// Every run's JUnit/HTML go to the object store (S3 when SWEATSHOP_S3_BUCKET
	// is set, else <line>/reports); OpenStore adds the line database for its
	// stats and per-test results.
	objects, err := report.ObjectStoreFromEnv(filepath.Join(l.Dir, "reports"))
	if err != nil {
		return nil, err
	}
	s.Reports = objects
	s.Deploy = &DeployState{Path: filepath.Join(l.Dir, "deploy.json")}
	s.Epics = &EpicApprovals{Path: filepath.Join(l.Dir, "approved-epics.json")}
	if tok, err := os.ReadFile(filepath.Join(l.Dir, "deploy.token")); err == nil {
		s.DeployToken = strings.TrimSpace(string(tok))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	s.reporter = &report.Reporter{Objects: objects}
	tokens := map[string]string{}
	for _, ls := range l.Stations {
		key, err := attest.LoadPrivateKey(ls.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("station %s: %w", ls.Name, err)
		}
		tok, err := ls.Token()
		if err != nil {
			return nil, fmt.Errorf("station %s: %w", ls.Name, err)
		}
		// The token is the station's identity; a shared or empty one would
		// let one agent act as another.
		if tok == "" {
			return nil, fmt.Errorf("station %s: empty token", ls.Name)
		}
		if other, dup := tokens[tok]; dup {
			return nil, fmt.Errorf("stations %s and %s share a token", other, ls.Name)
		}
		tokens[tok] = ls.Name
		s.Stations = append(s.Stations, &Station{
			Name: ls.Name, Role: ls.Role, AgentID: ls.AgentID, Token: tok, StateDir: ls.StateDir,
			Witness: &witness.Witness{
				Key: key, Role: ls.Role, AgentID: ls.AgentID,
				Constitution: c, ConstitutionDigest: digest,
				Runner:   witness.NewRunner(c.Runner, witness.LogRunner{Dir: filepath.Join(ls.StateDir, "evidence")}),
				CacheDir: filepath.Join(ls.StateDir, "cache.git"),
				Upstream: l.Remote,
				Reporter: s.reporter,
			},
		})
	}
	return s, nil
}

// OpenStore opens the line database in dir (importing the flat files an older
// line kept), serves the run, stats, kaizen signals and heijunka levels routes
// from it and records every witness run in it. `line serve` calls it once: it
// is the database's only writer, so commands that merely load the server
// (viceroy start) must not.
func (s *Server) OpenStore(ctx context.Context, dir string) error {
	store, err := linestore.Open(ctx, filepath.Join(dir, linestore.FileName))
	if err != nil {
		return err
	}
	if err := store.ImportLegacy(ctx, dir); err != nil {
		store.Close()
		return err
	}
	s.Store = store
	s.reporter.Runs, s.reporter.Tests, s.reporter.Checks = store, store, store
	return nil
}

// Close releases the line database. Call it after the server has stopped serving.
func (s *Server) Close() error {
	if s.Store == nil {
		return nil
	}
	return s.Store.Close()
}

// httpError carries the status code a handler wants for a failure.
type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }

func fail(code int, format string, args ...any) error {
	return &httpError{code: code, msg: fmt.Sprintf(format, args...)}
}

// requireMethod answers a request whose method is not want with 405 and an
// Allow header.
func requireMethod(w http.ResponseWriter, r *http.Request, want string) error {
	if r.Method != want {
		w.Header().Set("Allow", want)
		return fail(http.StatusMethodNotAllowed, "use %s for %s", want, r.URL.Path)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// authenticate compares against every station so timing does not reveal
// which (or whether a) token matched.
func (s *Server) authenticate(r *http.Request) *Station {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return nil
	}
	var found *Station
	for _, st := range s.Stations {
		if st.Token != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(st.Token)) == 1 {
			found = st
		}
	}
	return found
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	commit := r.URL.Query().Get("commit")
	st := s.authenticate(r)
	if st == nil {
		st = s.deployerStation(r)
	}
	if st == nil {
		log.Printf("station api: station=- route=%q commit=%q outcome=unauthorized", r.Method+" "+r.URL.Path, commit)
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	outcome, err := s.route(w, r, st)
	if err != nil {
		code := http.StatusInternalServerError
		clientMsg := "internal error"
		var he *httpError
		if errors.As(err, &he) {
			code = he.code
			clientMsg = err.Error()
		}
		// Unclassified errors (os.PathError and the like) embed host paths,
		// so only the log gets their text.
		outcome = fmt.Sprintf("%d %v", code, err)
		writeError(w, code, clientMsg)
	}
	log.Printf("station api: station=%s route=%q commit=%q outcome=%q", st.Name, r.Method+" "+r.URL.Path, commit, outcome)
}

// route dispatches an authenticated request. A handler either writes its
// response and returns an outcome for the log, or returns an error for
// ServeHTTP to write.
func (s *Server) route(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	path := r.URL.Path
	switch {
	case path == "/v1/attest" || path == "/v1/merge":
		if err := requireMethod(w, r, http.MethodPost); err != nil {
			return "", err
		}
		if !s.acquire() {
			return "", errBusy
		}
		defer s.busy.Store(false)
		return s.bundle(w, r, st, path == "/v1/merge")
	case path == "/v1/attest-line":
		if err := requireMethod(w, r, http.MethodPost); err != nil {
			return "", err
		}
		if !s.acquire() {
			return "", errBusy
		}
		defer s.busy.Store(false)
		return s.attestLine(w, r, st)
	case path == "/v1/andon/pull" || path == "/v1/andon/clear":
		if err := requireMethod(w, r, http.MethodPost); err != nil {
			return "", err
		}
		return s.andon(w, r, st, path == "/v1/andon/pull")
	case path == "/v1/status":
		if err := requireMethod(w, r, http.MethodGet); err != nil {
			return "", err
		}
		return s.status(w, r)
	case path == "/v1/stations/heartbeat":
		return s.heartbeat(w, r, st)
	case path == "/v1/runs":
		if err := requireMethod(w, r, http.MethodGet); err != nil {
			return "", err
		}
		return s.runs(w, r)
	case path == "/v1/stats" || path == "/metrics":
		if err := requireMethod(w, r, http.MethodGet); err != nil {
			return "", err
		}
		return s.ciStats(w, r, path == "/metrics")
	case path == "/v1/ci/stats":
		if err := requireMethod(w, r, http.MethodGet); err != nil {
			return "", err
		}
		return s.checkStats(w, r)
	case path == "/v1/cards" || strings.HasPrefix(path, "/v1/cards/"):
		return s.routeCards(w, r, st)
	case path == "/v1/constitution" || strings.HasPrefix(path, "/v1/constitution/"):
		return s.routeConstitution(w, r, st)
	case path == "/v1/testament":
		return s.routeTestament(w, r, st)
	case path == "/v1/reports" || path == "/v1/reports/object" || path == "/v1/reports/presign":
		return s.routeReports(w, r, st)
	case path == "/v1/usage":
		return s.routeUsage(w, r, st)
	case path == "/v1/events":
		return s.routeEvents(w, r, st)
	case path == "/v1/logs":
		return s.routeLogs(w, r, st)
	case path == "/v1/heijunka/levels":
		return s.routeHeijunkaLevels(w, r, st)
	case path == "/v1/heijunka/state":
		return s.routeHeijunkaState(w, r)
	case strings.HasPrefix(path, "/v1/kaizen/"):
		return s.routeKaizen(w, r, st)
	case strings.HasPrefix(path, "/v1/acceptance/"):
		return s.routeAcceptance(w, r, st)
	case strings.HasPrefix(path, "/v1/deploy/"):
		return s.routeDeploy(w, r, st)
	case strings.HasPrefix(path, "/v1/viceroy/"):
		return s.routeViceroy(w, r, st)
	case strings.HasPrefix(path, "/v1/channel/"):
		return s.routeChannel(w, r, st)
	case strings.HasPrefix(path, "/v1/evidence/"):
		if err := requireMethod(w, r, http.MethodGet); err != nil {
			return "", err
		}
		return s.evidence(w, strings.TrimPrefix(path, "/v1/evidence/"))
	default:
		return "", fail(http.StatusNotFound, "no route %s", path)
	}
}

var fullHex = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

func commitParam(r *http.Request) (string, error) {
	c := r.URL.Query().Get("commit")
	if !fullHex.MatchString(c) {
		return "", fail(http.StatusBadRequest, "commit %q is not a full lowercase hex object id", c)
	}
	return c, nil
}

func (s *Server) maxBundle() int64 {
	if s.MaxBundle > 0 {
		return s.MaxBundle
	}
	return defaultMaxBundle
}

// receive streams the request body to a private temp file in the station's
// state directory. The caller must call cleanup.
func (s *Server) receive(w http.ResponseWriter, r *http.Request, st *Station) (string, func(), error) {
	limit := s.maxBundle()
	tooLarge := fail(http.StatusRequestEntityTooLarge, "bundle exceeds %d bytes", limit)
	if r.ContentLength > limit {
		return "", nil, tooLarge
	}
	dir := st.StateDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp(dir, "upload-*.bundle") // mode 0600
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	_, err = io.Copy(f, http.MaxBytesReader(w, r.Body, limit))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		cleanup()
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return "", nil, tooLarge
		}
		return "", nil, fmt.Errorf("receive bundle: %w", err)
	}
	return f.Name(), cleanup, nil
}

func (s *Server) bundle(w http.ResponseWriter, r *http.Request, st *Station, merge bool) (string, error) {
	commit, err := commitParam(r)
	if err != nil {
		return "", err
	}
	path, cleanup, err := s.receive(w, r, st)
	if err != nil {
		return "", err
	}
	defer cleanup()
	if merge {
		res, err := st.Witness.MergeBundle(r.Context(), path, commit, s.Guard)
		if err != nil {
			return "", fail(http.StatusUnprocessableEntity, "%v", err)
		}
		s.recordEvent(r.Context(), st, linestore.EventSubmit, "", fmt.Sprintf("commit %s passed=%t guard=%s %s", commit, res.Attestation.Passed, res.Guard.Status, res.Guard.Reason))
		if res.Guard.Status == guard.Merged {
			s.recordEvent(r.Context(), st, linestore.EventMerge, "", "commit "+commit)
		}
		writeJSON(w, http.StatusOK, res)
		return fmt.Sprintf("passed=%t guard=%s %s", res.Attestation.Passed, res.Guard.Status, res.Guard.Reason), nil
	}
	res, err := st.Witness.AttestBundle(r.Context(), path, commit)
	return replyAttest(w, res, err)
}

func (s *Server) attestLine(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	commit, err := commitParam(r)
	if err != nil {
		return "", err
	}
	res, err := st.Witness.AttestLine(r.Context(), commit)
	return replyAttest(w, res, err)
}

// replyAttest answers an attest route: a witness error is a 422, a failing
// attestation is still a 200 result.
func replyAttest(w http.ResponseWriter, res witness.AttestResult, err error) (string, error) {
	if err != nil {
		return "", fail(http.StatusUnprocessableEntity, "%v", err)
	}
	writeJSON(w, http.StatusOK, res)
	return fmt.Sprintf("passed=%t", res.Passed), nil
}

// andon pulls or clears the cord. Guard rejections are results (200); only
// failures to reach a verdict are errors.
func (s *Server) andon(w http.ResponseWriter, r *http.Request, st *Station, pull bool) (string, error) {
	var env attest.Envelope
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEnvelope)).Decode(&env); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return "", fail(http.StatusRequestEntityTooLarge, "envelope exceeds %d bytes", maxEnvelope)
		}
		return "", fail(http.StatusBadRequest, "decode envelope: %v", err)
	}
	var res guard.Result
	var err error
	if pull {
		res, err = s.Guard.Pull(r.Context(), env)
	} else {
		res, err = s.Guard.Clear(r.Context(), env)
	}
	if err != nil {
		return "", err
	}
	kind := linestore.EventAndonClear
	if pull {
		kind = linestore.EventAndonPull
	}
	s.recordEvent(r.Context(), st, kind, "", fmt.Sprintf("guard=%s %s event=%s", res.Status, res.Reason, res.Event))
	writeJSON(w, http.StatusOK, res)
	return fmt.Sprintf("guard=%s %s event=%s", res.Status, res.Reason, res.Event), nil
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) (string, error) {
	master, err := s.Guard.Repo.Ref(r.Context(), s.Guard.Ref)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", s.Guard.Ref, err)
	}
	id, event, err := s.Guard.Stopped(r.Context())
	if err != nil {
		return "", err
	}
	// Status carries andon state makers depend on, so a card tracker failure
	// is logged rather than failing the route.
	held, err := s.heldCards(r.Context())
	if err != nil {
		log.Printf("status: held cards: %v", err)
	}
	budgets, _, err := s.currentBudgets(r.Context(), time.Now())
	if err != nil {
		log.Printf("status: token budgets: %v", err)
	}
	writeJSON(w, http.StatusOK, Status{Master: master, Event: id, Andon: event, Stations: s.liveness.snapshot(s.Stations, time.Now()), KaizenRequest: s.latestKaizenRequest(), Held: held, Budgets: budgets})
	return "master=" + master + " event=" + id, nil
}

// evidence serves any station's check log: andon and makers need each
// other's evidence to triage a stop.
func (s *Server) evidence(w http.ResponseWriter, id string) (string, error) {
	for _, st := range s.Stations {
		data, err := witness.ReadEvidence(s.EvidenceDir(st), id)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			// ReadEvidence rejects malformed ids before touching the disk.
			return "", fail(http.StatusBadRequest, "%v", err)
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data)
		return fmt.Sprintf("evidence %s (%d bytes)", id, len(data)), nil
	}
	return "", fail(http.StatusNotFound, "no evidence %q", id)
}
