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
	"strings"
	"sync/atomic"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/linedir"
	"github.com/Fewbytes/sweatshop/line/report"
	"github.com/Fewbytes/sweatshop/line/witness"
)

const (
	defaultMaxBundle = 256 << 20
	// maxEnvelope bounds andon request bodies; an envelope is a few KiB.
	maxEnvelope = 1 << 20
)

type Station struct {
	Name, Role, AgentID string
	Token               string
	Witness             *witness.Witness
	// StateDir receives bundle uploads while they are attested; "" means the
	// system temp directory.
	StateDir string
}

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

	// busy admits one check-running request (attest, merge, attest-line) at a
	// time across all stations, so several tokens cannot run checks in
	// parallel to exhaust the host.
	busy atomic.Bool
}

// acquire claims the line's single check slot; release with s.busy.Store(false).
func (s *Server) acquire() bool { return s.busy.CompareAndSwap(false, true) }

type Status struct {
	Master string            `json:"master"`
	Event  string            `json:"event,omitempty"`
	Andon  *guard.AndonEvent `json:"andon,omitempty"`
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
		Guard: guard.Guard{Repo: repo, Ref: "refs/heads/master", Constitution: c, ConstitutionDigest: digest},
		EvidenceDir: func(st *Station) string {
			return filepath.Join(st.StateDir, "evidence")
		},
	}
	// Every run's JUnit/HTML go to the object store (S3 when SWEATSHOP_S3_BUCKET
	// is set, else <line>/reports) and its stats to <line>/runs.jsonl.
	objects, err := report.ObjectStoreFromEnv(filepath.Join(l.Dir, "reports"))
	if err != nil {
		return nil, err
	}
	reporter := &report.Reporter{Objects: objects, Runs: &report.JSONLRunStore{Path: filepath.Join(l.Dir, "runs.jsonl")}}
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
				Reporter: reporter,
			},
		})
	}
	return s, nil
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
		log.Printf("station api: station=- route=%q commit=%q outcome=unauthorized", r.Method+" "+r.URL.Path, commit)
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	outcome, err := s.route(w, r, st)
	if err != nil {
		code := http.StatusInternalServerError
		var he *httpError
		if errors.As(err, &he) {
			code = he.code
		}
		outcome = fmt.Sprintf("%d %v", code, err)
		writeError(w, code, err.Error())
	}
	log.Printf("station api: station=%s route=%q commit=%q outcome=%q", st.Name, r.Method+" "+r.URL.Path, commit, outcome)
}

// route dispatches an authenticated request. A handler either writes its
// response and returns an outcome for the log, or returns an error for
// ServeHTTP to write.
func (s *Server) route(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	method := func(want string) error {
		if r.Method != want {
			w.Header().Set("Allow", want)
			return fail(http.StatusMethodNotAllowed, "use %s for %s", want, r.URL.Path)
		}
		return nil
	}
	path := r.URL.Path
	switch {
	case path == "/v1/attest" || path == "/v1/merge":
		if err := method(http.MethodPost); err != nil {
			return "", err
		}
		if !s.acquire() {
			return "", errBusy
		}
		defer s.busy.Store(false)
		return s.bundle(w, r, st, path == "/v1/merge")
	case path == "/v1/attest-line":
		if err := method(http.MethodPost); err != nil {
			return "", err
		}
		if !s.acquire() {
			return "", errBusy
		}
		defer s.busy.Store(false)
		return s.attestLine(w, r, st)
	case path == "/v1/andon/pull" || path == "/v1/andon/clear":
		if err := method(http.MethodPost); err != nil {
			return "", err
		}
		return s.andon(w, r, path == "/v1/andon/pull")
	case path == "/v1/status":
		if err := method(http.MethodGet); err != nil {
			return "", err
		}
		return s.status(w, r)
	case path == "/v1/cards" || strings.HasPrefix(path, "/v1/cards/"):
		return s.routeCards(w, r, st)
	case path == "/v1/constitution" || strings.HasPrefix(path, "/v1/constitution/"):
		return s.routeConstitution(w, r, st)
	case path == "/v1/testament":
		return s.routeTestament(w, r, st)
	case strings.HasPrefix(path, "/v1/viceroy/"):
		return s.routeViceroy(w, r, st)
	case strings.HasPrefix(path, "/v1/channel/"):
		return s.routeChannel(w, r, st)
	case strings.HasPrefix(path, "/v1/evidence/"):
		if err := method(http.MethodGet); err != nil {
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
		writeJSON(w, http.StatusOK, res)
		return fmt.Sprintf("passed=%t guard=%s %s", res.Attestation.Passed, res.Guard.Status, res.Guard.Reason), nil
	}
	res, err := st.Witness.AttestBundle(r.Context(), path, commit)
	if err != nil {
		return "", fail(http.StatusUnprocessableEntity, "%v", err)
	}
	writeJSON(w, http.StatusOK, res)
	return fmt.Sprintf("passed=%t", res.Passed), nil
}

func (s *Server) attestLine(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	commit, err := commitParam(r)
	if err != nil {
		return "", err
	}
	res, err := st.Witness.AttestLine(r.Context(), commit)
	if err != nil {
		return "", fail(http.StatusUnprocessableEntity, "%v", err)
	}
	writeJSON(w, http.StatusOK, res)
	return fmt.Sprintf("passed=%t", res.Passed), nil
}

// andon pulls or clears the cord. Guard rejections are results (200); only
// failures to reach a verdict are errors.
func (s *Server) andon(w http.ResponseWriter, r *http.Request, pull bool) (string, error) {
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
	writeJSON(w, http.StatusOK, Status{Master: master, Event: id, Andon: event})
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
