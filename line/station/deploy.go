package station

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Fewbytes/sweatshop/line/deploy"
	"github.com/Fewbytes/sweatshop/line/report"
)

const (
	deployerName  = "deployer"
	shortLen      = 12
	minShort      = 7
	maxStatusBody = 64 << 10
)

var approveDeployRe = regexp.MustCompile(`^approve deploy ([0-9a-f]{7,64})$`)

// ReleaseBuilder builds the host component image for a master commit and
// returns the release with its digest recorded. Nil means the line deploys
// commits only (the launchd backend builds its own binary from the commit).
type ReleaseBuilder interface {
	Build(ctx context.Context, commit string) (deploy.Release, error)
}

// DeployRequest is one asked-for redeploy.
type DeployRequest struct {
	Release     deploy.Release `json:"release"`
	RequestedAt time.Time      `json:"requested_at"`
	RequestedBy string         `json:"requested_by"`
	Approved    bool           `json:"approved"`
	ApprovedBy  string         `json:"approved_by,omitempty"` // the king's message ID
}

// DeployState is the line's deploy bookkeeping, persisted as JSON.
type DeployState struct {
	Path string

	mu sync.Mutex
}

type deployFile struct {
	Requests []DeployRequest `json:"requests"`
	Desired  string          `json:"desired,omitempty"` // commit of the approved release
	Status   *deploy.Status  `json:"status,omitempty"`
}

func (d *DeployState) load() (deployFile, error) {
	var f deployFile
	data, err := os.ReadFile(d.Path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	return f, json.Unmarshal(data, &f)
}

func (d *DeployState) save(f deployFile) error {
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(d.Path), 0o700); err != nil {
		return err
	}
	tmp := d.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, d.Path)
}

// update runs fn on the loaded state under the lock and saves the result.
func (d *DeployState) update(fn func(*deployFile) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, err := d.load()
	if err != nil {
		return err
	}
	if err := fn(&f); err != nil {
		return err
	}
	return d.save(f)
}

func (d *DeployState) read() (deployFile, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.load()
}

func (s *Server) post(ctx context.Context, text string) error {
	if s.Channel == nil {
		return nil
	}
	return s.Channel.Discord.Post(ctx, s.Channel.ChannelID, text)
}

// attested reports whether a witness run passed on commit.
func (s *Server) attested(commit string) (bool, error) {
	runs, err := report.ReadJSONL(s.RunsPath)
	if err != nil {
		return false, err
	}
	for _, r := range runs {
		if r.Commit == commit && r.Passed {
			return true, nil
		}
	}
	return false, nil
}

// RequestDeploy records a request to redeploy the line to commit and asks the
// king. Only an attested master, with the andon clear, can be deployed.
func (s *Server) RequestDeploy(ctx context.Context, commit, by string) (DeployRequest, error) {
	g := s.Guard
	master, err := g.Repo.Ref(ctx, g.Ref)
	if err != nil {
		return DeployRequest{}, fmt.Errorf("read %s: %w", g.Ref, err)
	}
	if master != commit {
		return DeployRequest{}, fail(http.StatusConflict, "%s is not the line's master (%s)", commit, master)
	}
	if id, _, err := g.Stopped(ctx); err != nil {
		return DeployRequest{}, err
	} else if id != "" {
		return DeployRequest{}, fail(http.StatusConflict, "the line is stopped (andon event %s)", id)
	}
	if ok, err := s.attested(commit); err != nil {
		return DeployRequest{}, err
	} else if !ok {
		return DeployRequest{}, fail(http.StatusConflict, "%s has no passing attestation", commit)
	}
	st, err := s.Deploy.read()
	if err != nil {
		return DeployRequest{}, err
	}
	for _, r := range st.Requests {
		if r.Release.Commit != commit {
			continue
		}
		failed := st.Status != nil && st.Status.Commit == commit && st.Status.State != deploy.StateApplied
		if !failed {
			return r, nil // already asked, approved or applied
		}
	}
	rel := deploy.Release{Commit: commit}
	if s.Releases != nil {
		if rel, err = s.Releases.Build(ctx, commit); err != nil {
			return DeployRequest{}, fail(http.StatusBadGateway, "build release: %v", err)
		}
		if err := rel.Validate(true); err != nil {
			return DeployRequest{}, fail(http.StatusBadGateway, "build release: %v", err)
		}
		if rel.Commit != commit {
			return DeployRequest{}, fail(http.StatusBadGateway, "build release: built %s, wanted %s", rel.Commit, commit)
		}
	}
	req := DeployRequest{Release: rel, RequestedAt: time.Now().UTC(), RequestedBy: by}
	err = s.Deploy.update(func(f *deployFile) error {
		kept := f.Requests[:0]
		for _, r := range f.Requests {
			if r.Release.Commit != commit {
				kept = append(kept, r)
			}
		}
		f.Requests = append(kept, req)
		return nil
	})
	if err != nil {
		return DeployRequest{}, err
	}
	text := fmt.Sprintf("redeploy line to %s? reply: approve deploy %s", commit[:shortLen], commit[:shortLen])
	if err := s.post(ctx, text); err != nil {
		return DeployRequest{}, fail(http.StatusBadGateway, "request stored but the channel post failed: %v", err)
	}
	return req, nil
}

// ApproveDeploy marks the pending request whose commit starts with short as
// approved and desired. The caller has already checked the author is the
// sovereign. It fails if master moved since the request.
func (s *Server) ApproveDeploy(ctx context.Context, short, msgID string) (string, error) {
	if len(short) < minShort {
		return "", fmt.Errorf("deploy id %q is too short", short)
	}
	master, err := s.Guard.Repo.Ref(ctx, s.Guard.Ref)
	if err != nil {
		return "", err
	}
	var commit string
	err = s.Deploy.update(func(f *deployFile) error {
		idx := -1
		for i, r := range f.Requests {
			if !r.Approved && strings.HasPrefix(r.Release.Commit, short) {
				if idx >= 0 {
					return fmt.Errorf("deploy id %s is ambiguous", short)
				}
				idx = i
			}
		}
		if idx < 0 {
			return fmt.Errorf("no pending deploy request %s", short)
		}
		r := &f.Requests[idx]
		if r.Release.Commit != master {
			return fmt.Errorf("deploy request %s is stale: master is now %s", short, master)
		}
		r.Approved, r.ApprovedBy = true, msgID
		f.Desired = r.Release.Commit
		commit = r.Release.Commit
		return nil
	})
	return commit, err
}

// routeDeploy handles POST /v1/deploy/requests, GET /v1/deploy/desired and
// GET|POST /v1/deploy/status.
func (s *Server) routeDeploy(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	if s.Deploy == nil {
		return "", fail(http.StatusServiceUnavailable, "deploy is not configured")
	}
	want := http.MethodGet
	switch r.URL.Path {
	case "/v1/deploy/requests":
		want = http.MethodPost
	case "/v1/deploy/status":
		if r.Method == http.MethodPost {
			want = http.MethodPost
		}
	case "/v1/deploy/desired":
	default:
		return "", fail(http.StatusNotFound, "no route %s", r.URL.Path)
	}
	if r.Method != want {
		w.Header().Set("Allow", want)
		return "", fail(http.StatusMethodNotAllowed, "use %s for %s", want, r.URL.Path)
	}
	ctx := r.Context()
	switch {
	case r.URL.Path == "/v1/deploy/requests":
		if st.Role != viceroyRole {
			return "", fail(http.StatusForbidden, "role %q may not request a deploy", st.Role)
		}
		commit, err := commitParam(r)
		if err != nil {
			return "", err
		}
		req, err := s.RequestDeploy(ctx, commit, st.Name)
		if err != nil {
			return "", err
		}
		writeJSON(w, http.StatusOK, req)
		return "deploy request " + commit, nil
	case r.URL.Path == "/v1/deploy/desired":
		f, err := s.Deploy.read()
		if err != nil {
			return "", err
		}
		out := deploy.DesiredResponse{}
		for _, req := range f.Requests {
			if req.Approved && req.Release.Commit == f.Desired {
				rel := req.Release
				out.Release = &rel
			}
		}
		writeJSON(w, http.StatusOK, out)
		return "deploy desired " + f.Desired, nil
	case r.Method == http.MethodGet:
		f, err := s.Deploy.read()
		if err != nil {
			return "", err
		}
		writeJSON(w, http.StatusOK, map[string]any{"desired": f.Desired, "status": f.Status})
		return "deploy status", nil
	}
	if st.Name != deployerName {
		return "", fail(http.StatusForbidden, "only the deployer reports deploy outcomes")
	}
	var status deploy.Status
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxStatusBody)).Decode(&status); err != nil {
		return "", fail(http.StatusBadRequest, "decode status: %v", err)
	}
	switch status.State {
	case deploy.StateApplied, deploy.StateRolledBack, deploy.StateFailed:
	default:
		return "", fail(http.StatusBadRequest, "unknown state %q", status.State)
	}
	err := s.Deploy.update(func(f *deployFile) error {
		if status.Commit == "" || status.Commit != f.Desired {
			return fail(http.StatusConflict, "%q is not the desired release", status.Commit)
		}
		f.Status = &status
		return nil
	})
	if err != nil {
		return "", err
	}
	text := fmt.Sprintf("deploy of line %s: %s", status.Commit[:min(len(status.Commit), shortLen)], status.State)
	if status.Detail != "" {
		text += " (" + status.Detail + ")"
	}
	if err := s.post(ctx, text); err != nil {
		return "", fail(http.StatusBadGateway, "status stored but the channel post failed: %v", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": status.State})
	return "deploy status " + status.State, nil
}

// deployerStation authenticates the actuator's token. It is no station: it
// reaches only the deploy routes and /v1/status (its health check).
func (s *Server) deployerStation(r *http.Request) *Station {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" || s.DeployToken == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(s.DeployToken)) != 1 {
		return nil
	}
	switch p := r.URL.Path; {
	case p == "/v1/status", strings.HasPrefix(p, "/v1/deploy/"):
		return &Station{Name: deployerName, Role: deployerName}
	}
	return nil
}
