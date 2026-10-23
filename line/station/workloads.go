package station

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/line/constitution"
)

const (
	controllerRole = "controller" // not in constitution.Roles, so no station can claim it
	// maxWorkloadBody bounds a request or status body.
	maxWorkloadBody = 64 << 10
	// maxWorkloadLogs is how much of a workload's log tail serve keeps.
	maxWorkloadLogs = 64 << 10
	// maxPurposeChars bounds the free text that is shown to the king.
	maxPurposeChars = 200
)

var (
	approveWorkloadRe = regexp.MustCompile(`^approve workload (wl-[0-9]+)$`)
	// pinnedImageRe is a reference pinned by digest, so a tag cannot move under an approval.
	pinnedImageRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)
	// resourceNameRe names a secret or volume; it excludes "/" and ":" so a
	// host path or a bind-mount spec cannot pass as a name.
	resourceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
	envNameRe      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// controllerStation authenticates the controller's token. It is no station:
// it reaches only the controller routes and /v1/status (its health check).
func (s *Server) controllerStation(r *http.Request) *Station {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" || s.ControllerToken == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(s.ControllerToken)) != 1 {
		return nil
	}
	if p := r.URL.Path; p == "/v1/status" || strings.HasPrefix(p, "/v1/controller/") {
		return &Station{Name: controllerRole, Role: controllerRole}
	}
	return nil
}

// seesWorkload reports whether st may read and stop w: its owner, and gemba for all.
func (st *Station) seesWorkload(w Workload) bool {
	return w.Station == st.Name || st.Role == gembaRole
}

// validateWorkloadSpec checks the shape of a request, independent of policy.
func validateWorkloadSpec(sp WorkloadSpec) error {
	if !slices.Contains(constitution.WorkloadKinds, sp.Kind) {
		return fmt.Errorf("kind %q: want one of %v", sp.Kind, constitution.WorkloadKinds)
	}
	switch {
	case sp.Image != "" && sp.Build != nil:
		return fmt.Errorf("give image or build, not both")
	case sp.Image == "" && sp.Build == nil:
		return fmt.Errorf("give image or build")
	case sp.Image != "" && !pinnedImageRe.MatchString(sp.Image):
		return fmt.Errorf("image %q: want a reference pinned by digest (name@sha256:<64 hex>)", sp.Image)
	}
	if b := sp.Build; b != nil {
		if !fullHex.MatchString(b.Commit) {
			return fmt.Errorf("build commit %q is not a full lowercase hex object id", b.Commit)
		}
		if b.Dockerfile == "" || !filepath.IsLocal(b.Dockerfile) {
			return fmt.Errorf("build dockerfile %q: want a path inside the commit", b.Dockerfile)
		}
	}
	for k := range sp.Env {
		if !envNameRe.MatchString(k) {
			return fmt.Errorf("env name %q is not a variable name", k)
		}
	}
	for _, n := range sp.Secrets {
		if !resourceNameRe.MatchString(n) {
			return fmt.Errorf("secret %q: want a name, not a path", n)
		}
	}
	for _, n := range sp.Volumes {
		if !resourceNameRe.MatchString(n) {
			return fmt.Errorf("volume %q: want a named volume, not a path", n)
		}
	}
	if sp.Resources.Pids < 0 {
		return fmt.Errorf("pids %d: must not be negative", sp.Resources.Pids)
	}
	if e := sp.Expose; e != nil && (e.Port < 1 || e.Port > 65535 || !resourceNameRe.MatchString(e.Name)) {
		return fmt.Errorf("expose %+v: want a port 1-65535 and a name", *e)
	}
	return nil
}

// resolveWorkload applies the rule to sp: it returns sp with the rule's bounds
// filled in, or the reason the request is outside the rule.
func resolveWorkload(rule constitution.WorkloadRule, sp WorkloadSpec) (WorkloadSpec, error) {
	if sp.Expose != nil && !rule.Expose {
		return sp, fmt.Errorf("the rule does not allow exposing a port")
	}
	ttl := rule.TTLDuration()
	if sp.TTL != "" {
		d, err := time.ParseDuration(sp.TTL)
		if err != nil || d <= 0 {
			return sp, fmt.Errorf("ttl %q: want a positive duration like 2h", sp.TTL)
		}
		if d > ttl {
			return sp, fmt.Errorf("ttl %s exceeds the rule's %s", d, ttl)
		}
		ttl = d
	}
	sp.TTL = ttl.String()

	res := &sp.Resources
	if err := boundResource("memory", constitution.ParseMemoryBytes, rule.Memory, &res.Memory); err != nil {
		return sp, err
	}
	if err := boundResource("cpus", constitution.ParseCPUCount, rule.CPUs, &res.CPUs); err != nil {
		return sp, err
	}
	if rule.Pids > 0 {
		if res.Pids == 0 {
			res.Pids = rule.Pids
		}
		if res.Pids > rule.Pids {
			return sp, fmt.Errorf("pids %d exceeds the rule's %d", res.Pids, rule.Pids)
		}
	}
	return sp, nil
}

// boundResource checks the requested resource string against the rule's bound
// (empty means unbounded): an empty request takes the bound, a non-empty one
// must parse and not exceed it. parse is the resource's own parser.
func boundResource[N int64 | float64](name string, parse func(string) (N, error), ruleBound string, requested *string) error {
	if ruleBound == "" {
		if *requested == "" {
			return nil
		}
		_, err := parse(*requested)
		return err
	}
	bound, _ := parse(ruleBound)
	if *requested == "" {
		*requested = ruleBound
	}
	n, err := parse(*requested)
	if err != nil {
		return err
	}
	if n > bound {
		return fmt.Errorf("%s %s exceeds the rule's %s", name, *requested, ruleBound)
	}
	return nil
}

// oneLine collapses whitespace and cuts s to max characters, for text shown in
// the channel.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// RequestWorkload checks spec against the constitution's [workloads] policy
// and the station's quota, records the request, and either approves it (rule
// approval "auto") or asks the king on the channel. A request the policy
// refuses is recorded as denied and returned with a 403 or 429 error, so the
// audit has it; a malformed one is a 400 and leaves no record.
func (s *Server) RequestWorkload(ctx context.Context, st *Station, spec WorkloadSpec) (Workload, error) {
	if err := validateWorkloadSpec(spec); err != nil {
		return Workload{}, fail(http.StatusBadRequest, "%v", err)
	}
	spec.Purpose = oneLine(spec.Purpose, maxPurposeChars)
	policy := s.Guard.Constitution.Workloads
	w := Workload{Station: st.Name, Role: st.Role, Rule: st.Role + "/" + spec.Kind, Spec: spec}
	denied, code := "", http.StatusForbidden
	var rule constitution.WorkloadRule
	var ruleFound bool
	if rule, ruleFound = policy.RuleFor(st.Role, spec.Kind); !ruleFound {
		denied = fmt.Sprintf("role %q has no workloads rule for kind %q", st.Role, spec.Kind)
	} else if spec.Image != "" && !policy.ImageAllowed(spec.Image) {
		denied = fmt.Sprintf("image %q is not in the allowed images", spec.Image)
	} else if resolved, err := resolveWorkload(rule, spec); err != nil {
		denied = err.Error()
	} else {
		w.Spec = resolved
	}

	now := s.Workloads.now()
	err := s.Workloads.update(func(f *workloadFile) error {
		if denied == "" && rule.MaxPerStation > 0 {
			live := 0
			for _, o := range f.Workloads {
				if o.Station == st.Name && o.live() && slices.Contains(rule.Kinds, o.Spec.Kind) {
					live++
				}
			}
			if live >= rule.MaxPerStation {
				denied, code = fmt.Sprintf("station %s already has %d live workloads; the rule allows %d", st.Name, live, rule.MaxPerStation), http.StatusTooManyRequests
			}
		}
		switch {
		case denied != "":
			w.setState(now, WorkloadDenied, "serve", denied)
		case rule.Approval == constitution.WorkloadApprovalAuto:
			ttl, _ := time.ParseDuration(w.Spec.TTL) // resolveWorkload set it
			w.ExpiresAt = now.Add(ttl)
			w.History = []WorkloadEvent{{At: now, State: WorkloadPending, By: st.Name, Detail: "requested under rule " + w.Rule}}
			w.setState(now, WorkloadApproved, "serve", "auto approval under rule "+w.Rule)
		default:
			w.setState(now, WorkloadPending, st.Name, "requested under rule "+w.Rule+"; waiting for the king")
		}
		w = f.add(w)
		return nil
	})
	if err != nil {
		return Workload{}, err
	}
	if denied != "" {
		return w, fail(code, "workload %s denied: %s", w.ID, denied)
	}
	if w.State == WorkloadPending {
		text := fmt.Sprintf("%s (%s) asks for a %s workload %s: %s. reply: approve workload %s",
			st.Name, st.Role, w.Spec.Kind, w.ID, oneLine(w.Spec.Purpose+" "+workloadTarget(w.Spec), maxPurposeChars+120), w.ID)
		if err := s.post(ctx, text); err != nil {
			return w, fail(http.StatusBadGateway, "request %s stored but the channel post failed: %v", w.ID, err)
		}
	}
	return w, nil
}

func workloadTarget(sp WorkloadSpec) string {
	if sp.Build != nil {
		return "build " + sp.Build.Commit[:min(len(sp.Build.Commit), shortLen)]
	}
	return sp.Image
}

// ApproveWorkload approves the pending workload id on the king's message and
// starts its lifetime. The caller has already checked the author is the king.
func (s *Server) ApproveWorkload(_ context.Context, id, msgID string) error {
	if s.Workloads == nil {
		return fmt.Errorf("workloads are not configured")
	}
	now := s.Workloads.now()
	return s.Workloads.update(func(f *workloadFile) error {
		i := f.find(id)
		if i < 0 || f.Workloads[i].State != WorkloadPending {
			return fmt.Errorf("no pending workload request %s", id)
		}
		w := &f.Workloads[i]
		d, err := time.ParseDuration(w.Spec.TTL)
		if err != nil {
			return fmt.Errorf("workload %s: %w", id, err)
		}
		w.ExpiresAt = now.Add(d)
		w.setState(now, WorkloadApproved, "king:"+msgID, "approved by the king")
		return nil
	})
}

// StopWorkload stops workload id for st, its owner (or gemba). A request still
// waiting for the king is withdrawn; otherwise the controller is told to remove
// the engine object.
func (s *Server) StopWorkload(st *Station, id string) (Workload, error) {
	var out Workload
	now := s.Workloads.now()
	err := s.Workloads.update(func(f *workloadFile) error {
		i := f.find(id)
		if i < 0 || !st.seesWorkload(f.Workloads[i]) {
			return fail(http.StatusNotFound, "no workload %s", id)
		}
		w := &f.Workloads[i]
		switch {
		case w.State == WorkloadPending:
			w.setState(now, WorkloadRemoved, st.Name, "withdrawn before approval")
		case w.controllerVisible() && !w.Stopped:
			w.Stopped = true
			w.History = append(w.History, WorkloadEvent{At: now, State: w.State, By: st.Name, Detail: "stop requested"})
		}
		out = *w
		return nil
	})
	return out, err
}

// routeWorkloads handles /v1/workloads and /v1/workloads/{id}[/logs].
func (s *Server) routeWorkloads(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	if s.Workloads == nil {
		return "", fail(http.StatusServiceUnavailable, "workloads are not configured")
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/v1/workloads"), "/")
	id, sub, _ := strings.Cut(rest, "/")
	switch {
	case rest == "":
		switch r.Method {
		case http.MethodPost:
			var spec WorkloadSpec
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkloadBody))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&spec); err != nil {
				return "", fail(http.StatusBadRequest, "decode workload request: %v", err)
			}
			wl, err := s.RequestWorkload(r.Context(), st, spec)
			if err != nil {
				return "", err
			}
			writeJSON(w, http.StatusCreated, wl)
			return "workload " + wl.ID + " " + wl.State, nil
		case http.MethodGet:
			f, err := s.Workloads.read()
			if err != nil {
				return "", err
			}
			out := []Workload{}
			for _, wl := range f.Workloads {
				if st.seesWorkload(wl) {
					out = append(out, wl)
				}
			}
			writeJSON(w, http.StatusOK, out)
			return "workloads list", nil
		}
		return "", requireMethod(w, r, http.MethodGet)
	case sub == "" || sub == "logs":
		want := http.MethodGet
		if sub == "" && r.Method == http.MethodDelete {
			want = http.MethodDelete
		}
		if err := requireMethod(w, r, want); err != nil {
			return "", err
		}
		if want == http.MethodDelete {
			wl, err := s.StopWorkload(st, id)
			if err != nil {
				return "", err
			}
			writeJSON(w, http.StatusOK, wl)
			return "workload " + id + " stop", nil
		}
		f, err := s.Workloads.read()
		if err != nil {
			return "", err
		}
		i := f.find(id)
		if i < 0 || !st.seesWorkload(f.Workloads[i]) {
			return "", fail(http.StatusNotFound, "no workload %s", id)
		}
		if sub == "" {
			writeJSON(w, http.StatusOK, f.Workloads[i])
			return "workload " + id, nil
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(f.Logs[id]))
		return "workload " + id + " logs", nil
	}
	return "", fail(http.StatusNotFound, "no route %s", r.URL.Path)
}

// WorkloadStatus is the controller's report on one workload.
type WorkloadStatus struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
	Logs   string `json:"logs,omitempty"` // the log tail so far; replaces the earlier one
}

// routeController handles GET /v1/controller/pending and POST
// /v1/controller/status. Only the controller reaches them.
func (s *Server) routeController(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	if s.Workloads == nil {
		return "", fail(http.StatusServiceUnavailable, "workloads are not configured")
	}
	if st.Role != controllerRole {
		return "", fail(http.StatusForbidden, "only the controller uses %s", r.URL.Path)
	}
	switch r.URL.Path {
	case "/v1/controller/pending":
		if err := requireMethod(w, r, http.MethodGet); err != nil {
			return "", err
		}
		f, err := s.Workloads.read()
		if err != nil {
			return "", err
		}
		out := []Workload{}
		for _, wl := range f.Workloads {
			if wl.controllerVisible() {
				out = append(out, wl)
			}
		}
		writeJSON(w, http.StatusOK, out)
		return "controller pending", nil
	case "/v1/controller/status":
		if err := requireMethod(w, r, http.MethodPost); err != nil {
			return "", err
		}
		var rep WorkloadStatus
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkloadBody)).Decode(&rep); err != nil {
			return "", fail(http.StatusBadRequest, "decode status: %v", err)
		}
		switch rep.State {
		case WorkloadCreating, WorkloadRunning, WorkloadExited, WorkloadFailed, WorkloadRemoved:
		default:
			return "", fail(http.StatusBadRequest, "state %q is not one the controller reports", rep.State)
		}
		now := s.Workloads.now()
		err := s.Workloads.update(func(f *workloadFile) error {
			i := f.find(rep.ID)
			if i < 0 || !f.Workloads[i].controllerVisible() {
				return fail(http.StatusConflict, "workload %q is not one the controller acts on", rep.ID)
			}
			f.Workloads[i].setState(now, rep.State, controllerRole, rep.Detail)
			if rep.Logs != "" {
				if f.Logs == nil {
					f.Logs = map[string]string{}
				}
				f.Logs[rep.ID] = rep.Logs[max(0, len(rep.Logs)-maxWorkloadLogs):]
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		writeJSON(w, http.StatusOK, map[string]string{"state": rep.State})
		return "controller status " + rep.ID + " " + rep.State, nil
	}
	return "", fail(http.StatusNotFound, "no route %s", r.URL.Path)
}
