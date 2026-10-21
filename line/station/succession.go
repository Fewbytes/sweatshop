package station

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/linedir"
	"github.com/Fewbytes/sweatshop/line/secret"
)

const (
	// GenHeader carries the caller's viceroy generation.
	GenHeader = "X-Sweatshop-Gen"
	// DefaultCheckinTimeout is how long a new generation has to check in.
	DefaultCheckinTimeout = 10 * time.Minute
	viceroyDockerfile     = "station/Dockerfile"
)

// ContainerSpec describes a detached viceroy container.
type ContainerSpec struct {
	Image, Name  string
	Env          map[string]string
	Memory, CPUs string
	Pids         int
	Mounts       []Mount
	Network      string // engine network to join; empty is the engine default
}

// Mount is a bind mount into the container.
type Mount struct {
	Host, Container string
	ReadOnly        bool
}

// Engine is the container engine the supervisor drives. Production uses
// CLIEngine; tests use a fake.
type Engine interface {
	// Build builds an image from contextDir with the given Dockerfile.
	Build(ctx context.Context, contextDir, dockerfile, tag string) error
	// Start starts the container detached.
	Start(ctx context.Context, spec ContainerSpec) error
	Running(ctx context.Context, name string) (bool, error)
	// Remove force-removes the container; a missing one is not an error.
	Remove(ctx context.Context, name string) error
}

// CLIEngine drives a docker-compatible CLI without a shell. Environment
// values travel through the CLI's own environment, not its argv.
type CLIEngine struct {
	Bin string // default "docker"
	Log *log.Logger
}

func (e CLIEngine) bin() string {
	if e.Bin == "" {
		return "docker"
	}
	return e.Bin
}

func (e CLIEngine) run(ctx context.Context, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, e.bin(), args...)
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", e.bin(), args[0], err, tailString(string(out), 20))
	}
	return string(out), nil
}

// Output runs the engine CLI with args and returns its output.
func (e CLIEngine) Output(ctx context.Context, args ...string) (string, error) {
	return e.run(ctx, nil, args...)
}

func tailString(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func (e CLIEngine) Build(ctx context.Context, contextDir, dockerfile, tag string) error {
	_, err := e.run(ctx, nil, "build", "-f", dockerfile, "-t", tag, contextDir)
	return err
}

func (e CLIEngine) Start(ctx context.Context, spec ContainerSpec) error {
	if spec.Name == "" || strings.HasPrefix(spec.Name, "-") {
		return fmt.Errorf("bad container name %q", spec.Name)
	}
	if spec.Image == "" || strings.HasPrefix(spec.Image, "-") {
		return fmt.Errorf("bad image %q", spec.Image)
	}
	args := []string{"run", "-d", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--name", spec.Name}
	if spec.Network != "" {
		args = append(args, "--network", spec.Network)
	}
	if spec.Memory != "" {
		args = append(args, "--memory", spec.Memory)
	}
	if spec.Pids > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(spec.Pids))
	}
	if spec.CPUs != "" {
		args = append(args, "--cpus", spec.CPUs)
	}
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			return fmt.Errorf("bad environment variable name %q", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := os.Environ()
	for _, k := range keys {
		args = append(args, "-e", k)
		env = append(env, k+"="+spec.Env[k])
	}
	for _, m := range spec.Mounts {
		if !filepath.IsAbs(m.Host) || !filepath.IsAbs(m.Container) || strings.Contains(m.Host+m.Container, ":") {
			return fmt.Errorf("bad mount %q -> %q", m.Host, m.Container)
		}
		v := m.Host + ":" + m.Container
		if m.ReadOnly {
			v += ":ro"
		}
		args = append(args, "-v", v)
	}
	_, err := e.run(ctx, env, append(args, spec.Image)...)
	return err
}

func (e CLIEngine) Running(ctx context.Context, name string) (bool, error) {
	out, err := exec.CommandContext(ctx, e.bin(), "inspect", "-f", "{{.State.Running}}", "--", name).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return false, nil // no such container
		}
		return false, err
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

func (e CLIEngine) Remove(ctx context.Context, name string) error {
	out, err := exec.CommandContext(ctx, e.bin(), "rm", "-f", "--", name).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "No such container") {
		return fmt.Errorf("%s rm: %w: %s", e.bin(), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Generation is one viceroy generation, as recorded in the line directory.
type Generation struct {
	Gen         int       `json:"gen"`
	Commit      string    `json:"commit"`
	Predecessor string    `json:"predecessor,omitempty"` // the previous generation's commit
	Station     string    `json:"station"`
	At          time.Time `json:"at"`
}

type pendingGen struct {
	Generation
	posted bool          // the new generation's one pre-check-in post was used
	done   chan struct{} // closed on check-in
}

// Supervisor builds and runs viceroy generations. It keeps no cache of the
// current generation: viceroy/current in the line directory is the record, so
// the owner's `line viceroy start` and a running `line serve` agree.
type Supervisor struct {
	Server  *Server
	Line    linedir.Line
	Engine  Engine
	URL     string // station API as seen from the container; default: url of line.toml, else http://host.containers.internal:<port>
	Log     *log.Logger
	Timeout time.Duration // check-in deadline; default DefaultCheckinTimeout
	Poll    time.Duration // how often a pending container is looked at; default 5s

	mu       sync.Mutex
	starting bool
	pending  *pendingGen
	watchers sync.WaitGroup
}

// NewSupervisor attaches a supervisor for l to s.
func NewSupervisor(s *Server, l linedir.Line, e Engine) *Supervisor {
	v := &Supervisor{Server: s, Line: l, Engine: e, Log: log.Default()}
	s.Viceroy = v
	return v
}

func (v *Supervisor) logf(format string, args ...any) {
	if v.Log != nil {
		v.Log.Printf(format, args...)
	}
}

func (v *Supervisor) dir() string { return filepath.Join(v.Line.Dir, "viceroy") }

// ContainerName is the container of generation gen.
func ContainerName(gen int) string { return "sweatshop-viceroy-" + strconv.Itoa(gen) }

func (v *Supervisor) current() (Generation, bool, error) {
	b, err := os.ReadFile(filepath.Join(v.dir(), "current"))
	if errors.Is(err, fs.ErrNotExist) {
		return Generation{}, false, nil
	}
	if err != nil {
		return Generation{}, false, err
	}
	var g Generation
	if err := json.Unmarshal(b, &g); err != nil {
		return Generation{}, false, fmt.Errorf("viceroy/current: %w", err)
	}
	return g, true, nil
}

func (v *Supervisor) record(g Generation) error {
	if err := os.MkdirAll(v.dir(), 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(g)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(v.dir(), "generations.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	tmp := filepath.Join(v.dir(), "current.tmp")
	if err := os.WriteFile(tmp, line, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(v.dir(), "current"))
}

// Admit is the liveness guard for channel posts and card writes: a request
// carrying the generation header of a non-current generation is refused,
// except the pending generation's single post.
func (v *Supervisor) Admit(r *http.Request, post bool) error {
	hdr := r.Header.Get(GenHeader)
	cur, have, err := v.current()
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if hdr == "" || (!have && v.pending == nil) {
		return nil
	}
	if have && hdr == strconv.Itoa(cur.Gen) {
		return nil
	}
	if p := v.pending; p != nil && hdr == strconv.Itoa(p.Gen) {
		if post && !p.posted {
			p.posted = true
			return nil
		}
		return fail(http.StatusConflict, "generation %s has not checked in", hdr)
	}
	return fail(http.StatusConflict, "generation %q is not the current viceroy generation", hdr)
}

// currentMasterIfSuperseded returns master when commit is one of its ancestors:
// the request queued for the check slot while merges moved master on, and the
// successor should run what the line now is. Any other commit is returned
// unchanged for checkMaster to refuse.
func (v *Supervisor) currentMasterIfSuperseded(ctx context.Context, commit string) (string, error) {
	g := v.Server.Guard
	master, err := g.Repo.Ref(ctx, g.Ref)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", g.Ref, err)
	}
	if ok, err := g.Repo.IsAncestor(ctx, commit, master); err != nil || !ok {
		return commit, nil
	}
	return master, nil
}

func (v *Supervisor) checkMaster(ctx context.Context, commit string) error {
	g := v.Server.Guard
	master, err := g.Repo.Ref(ctx, g.Ref)
	if err != nil {
		return fmt.Errorf("read %s: %w", g.Ref, err)
	}
	if master != commit {
		return fail(http.StatusConflict, "%s is not the line's master (%s)", commit, master)
	}
	id, _, err := g.Stopped(ctx)
	if err != nil {
		return err
	}
	if id != "" {
		return fail(http.StatusConflict, "the line is stopped (andon event %s)", id)
	}
	return nil
}

// checkout materialises commit (which is master) in a fresh directory.
func (v *Supervisor) checkout(ctx context.Context, commit string) (string, func(), error) {
	return checkoutCommit(ctx, v.Server.Guard.Repo, commit)
}

func (v *Supervisor) spec(st *Station, g Generation) (ContainerSpec, error) {
	token, err := secret.ForLine(v.Line.Dir).Read(secret.ModelCred)
	if err != nil {
		return ContainerSpec{}, fmt.Errorf("model credential: %w", err)
	}
	url := v.URL
	if url == "" {
		url = v.Line.StationURL()
	}
	env := map[string]string{
		"SWEATSHOP_MODE":          "viceroy",
		"SWEATSHOP_VICEROY_GEN":   strconv.Itoa(g.Gen),
		"SWEATSHOP_LINE_URL":      url,
		"SWEATSHOP_LINE_TOKEN":    st.Token,
		"SWEATSHOP_AGENT_ID":      st.AgentID,
		"CLAUDE_CODE_OAUTH_TOKEN": token,
	}
	if g.Predecessor != "" {
		env["SWEATSHOP_PREDECESSOR"] = g.Predecessor
	}
	image := v.Line.ViceroyImage
	if image == "" {
		image = "sweatshop-viceroy:" + g.Commit
	}
	return ContainerSpec{
		Image: image, Name: ContainerName(g.Gen), Env: env,
		Memory: constitution.DefaultMemory, Pids: constitution.DefaultPids, CPUs: constitution.DefaultCPUs,
		Mounts:  []Mount{{Host: v.Line.Remote, Container: "/line/remote.git", ReadOnly: true}},
		Network: v.Line.Network,
	}, nil
}

// launch attests, builds and starts generation g. The caller holds the
// starting flag and the line's check slot.
func (v *Supervisor) launch(ctx context.Context, st *Station, g Generation) error {
	res, err := st.Witness.AttestLine(ctx, g.Commit)
	if err != nil {
		return fail(http.StatusUnprocessableEntity, "attest %s: %v", g.Commit, err)
	}
	if !res.Passed {
		return fail(http.StatusUnprocessableEntity, "checks failed on %s", g.Commit)
	}
	spec, err := v.spec(st, g)
	if err != nil {
		return err
	}
	if v.Line.ViceroyImage == "" {
		dir, cleanup, err := v.checkout(ctx, g.Commit)
		if err != nil {
			return fmt.Errorf("checkout %s: %w", g.Commit, err)
		}
		defer cleanup()
		if err := v.Engine.Build(ctx, dir, filepath.Join(dir, viceroyDockerfile), spec.Image); err != nil {
			return fail(http.StatusBadGateway, "build: %v", err)
		}
	}
	_ = v.Engine.Remove(ctx, spec.Name) // a stale container of that name
	if err := v.Engine.Start(ctx, spec); err != nil {
		return fail(http.StatusBadGateway, "start: %v", err)
	}
	return nil
}

func (v *Supervisor) begin() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.starting || v.pending != nil {
		return fail(http.StatusConflict, "a succession is already in progress")
	}
	v.starting = true
	return nil
}

func (v *Supervisor) end() {
	v.mu.Lock()
	v.starting = false
	v.mu.Unlock()
}

// Bootstrap starts generation 1 (owner only): there must be no generation yet.
func (v *Supervisor) Bootstrap(ctx context.Context, st *Station, commit string) (Generation, error) {
	if err := v.begin(); err != nil {
		return Generation{}, err
	}
	defer v.end()
	if cur, have, err := v.current(); err != nil {
		return Generation{}, err
	} else if have {
		// Generation 1 has no predecessor to roll back to, so a recorded
		// generation whose container is not running is replaced.
		running, err := v.Engine.Running(ctx, ContainerName(cur.Gen))
		if err != nil {
			return Generation{}, fail(http.StatusBadGateway, "inspect generation %d: %v", cur.Gen, err)
		}
		if running || cur.Gen != 1 {
			return Generation{}, fail(http.StatusConflict, "the line already has a viceroy generation")
		}
		v.logf("viceroy: replacing generation 1 (%s): its container is not running", cur.Commit)
	}
	if err := v.checkMaster(ctx, commit); err != nil {
		return Generation{}, err
	}
	g := Generation{Gen: 1, Commit: commit, Station: st.Name, At: time.Now().UTC()}
	if err := v.launch(ctx, st, g); err != nil {
		return Generation{}, err
	}
	if err := v.record(g); err != nil {
		_ = v.Engine.Remove(ctx, ContainerName(g.Gen))
		return Generation{}, err
	}
	return g, nil
}

// Succeed starts the next generation at commit and waits, in the background,
// for its check-in.
func (v *Supervisor) Succeed(ctx context.Context, st *Station, commit string) (Generation, error) {
	if !v.Server.Guard.Constitution.Viceroy.Succession {
		return Generation{}, fail(http.StatusForbidden, "the constitution does not allow viceroy succession")
	}
	if err := v.begin(); err != nil {
		return Generation{}, err
	}
	defer v.end()
	cur, have, err := v.current()
	if err != nil {
		return Generation{}, err
	}
	if !have {
		return Generation{}, fail(http.StatusConflict, "no current viceroy generation; the owner starts generation 1")
	}
	if commit == cur.Commit {
		return Generation{}, fail(http.StatusConflict, "%s is already generation %d's commit", commit, cur.Gen)
	}
	commit, err = v.currentMasterIfSuperseded(ctx, commit)
	if err != nil {
		return Generation{}, err
	}
	if err := v.checkMaster(ctx, commit); err != nil {
		return Generation{}, err
	}
	g := Generation{Gen: cur.Gen + 1, Commit: commit, Predecessor: cur.Commit, Station: st.Name, At: time.Now().UTC()}
	if err := v.launch(ctx, st, g); err != nil {
		return Generation{}, err
	}
	p := &pendingGen{Generation: g, done: make(chan struct{})}
	v.mu.Lock()
	v.pending = p
	v.mu.Unlock()
	v.watchers.Add(1)
	go v.watch(p)
	return g, nil
}

// watch gives the pending generation until the deadline to check in, then
// rolls back.
func (v *Supervisor) watch(p *pendingGen) {
	defer v.watchers.Done()
	timeout, poll := v.Timeout, v.Poll
	if timeout <= 0 {
		timeout = DefaultCheckinTimeout
	}
	if poll <= 0 {
		poll = 5 * time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-deadline.C:
			v.rollback(p, fmt.Sprintf("no check-in within %s", timeout))
			return
		case <-tick.C:
			running, err := v.Engine.Running(context.Background(), ContainerName(p.Gen))
			if err == nil && !running {
				v.rollback(p, "the container exited before checking in")
				return
			}
		}
	}
}

func (v *Supervisor) rollback(p *pendingGen, why string) {
	v.mu.Lock()
	if v.pending != p {
		v.mu.Unlock()
		return
	}
	v.pending = nil
	v.mu.Unlock()
	ctx := context.Background()
	if err := v.Engine.Remove(ctx, ContainerName(p.Gen)); err != nil {
		v.logf("viceroy: remove generation %d: %v", p.Gen, err)
	}
	msg := fmt.Sprintf("Viceroy succession to %s failed (%s). Generation %d stays in charge.", p.Commit, why, p.Gen-1)
	v.logf("viceroy: %s", msg)
	if ch := v.Server.Channel; ch != nil {
		if err := ch.Discord.Post(ctx, ch.ChannelID, msg); err != nil {
			v.logf("viceroy: post failure: %v", err)
		}
	}
}

// Checkin completes a pending succession: the old container goes, the new
// generation becomes current. With nothing pending it is idempotent for the
// current generation, which checks in again after Resume restarts it.
func (v *Supervisor) Checkin(ctx context.Context, gen string) (Generation, error) {
	v.mu.Lock()
	p := v.pending
	v.mu.Unlock()
	if p == nil {
		cur, have, err := v.current()
		if err != nil {
			return Generation{}, err
		}
		if have && gen == strconv.Itoa(cur.Gen) {
			return cur, nil
		}
		return Generation{}, fail(http.StatusConflict, "no succession is pending")
	}
	if gen != strconv.Itoa(p.Gen) {
		return Generation{}, fail(http.StatusConflict, "generation %q is not the pending generation %d", gen, p.Gen)
	}
	if err := v.Engine.Remove(ctx, ContainerName(p.Gen-1)); err != nil {
		return Generation{}, fail(http.StatusBadGateway, "stop generation %d: %v", p.Gen-1, err)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.pending != p {
		return Generation{}, fail(http.StatusConflict, "succession to generation %d was rolled back", p.Gen)
	}
	if err := v.record(p.Generation); err != nil {
		return Generation{}, err
	}
	v.pending = nil
	close(p.done)
	return p.Generation, nil
}

// Resume starts the current generation if its container is not running.
func (v *Supervisor) Resume(ctx context.Context) error {
	g, have, err := v.current()
	if err != nil || !have {
		return err
	}
	if running, err := v.Engine.Running(ctx, ContainerName(g.Gen)); err != nil {
		return err
	} else if running {
		return nil
	}
	var st *Station
	for _, s := range v.Server.Stations {
		if s.Name == g.Station {
			st = s
		}
	}
	if st == nil {
		return fmt.Errorf("viceroy generation %d: no station %q", g.Gen, g.Station)
	}
	spec, err := v.spec(st, g)
	if err != nil {
		return err
	}
	_ = v.Engine.Remove(ctx, spec.Name)
	if err := v.Engine.Start(ctx, spec); err != nil {
		return err
	}
	v.logf("viceroy: restarted generation %d (%s)", g.Gen, g.Commit)
	return nil
}

// routeViceroy handles /v1/viceroy/succeed and /v1/viceroy/checkin.
func (s *Server) routeViceroy(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	path := r.URL.Path
	if path != "/v1/viceroy/succeed" && path != "/v1/viceroy/checkin" {
		return "", fail(http.StatusNotFound, "no route %s", path)
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		return "", fail(http.StatusMethodNotAllowed, "use POST for %s", path)
	}
	if st.Role != viceroyRole {
		return "", fail(http.StatusForbidden, "role %q may not manage the viceroy", st.Role)
	}
	v := s.Viceroy
	if v == nil {
		return "", fail(http.StatusServiceUnavailable, "viceroy supervisor is off")
	}
	if path == "/v1/viceroy/checkin" {
		g, err := v.Checkin(r.Context(), r.Header.Get(GenHeader))
		if err != nil {
			return "", err
		}
		writeJSON(w, http.StatusOK, g)
		return fmt.Sprintf("checkin generation %d", g.Gen), nil
	}
	commit, err := commitParam(r)
	if err != nil {
		return "", err
	}
	if err := v.Admit(r, false); err != nil {
		return "", err
	}
	if err := s.acquireQueued(r.Context(), successionSlotWait); err != nil {
		return "", err
	}
	defer s.busy.Store(false)
	g, err := v.Succeed(r.Context(), st, commit)
	if err != nil {
		return "", err
	}
	writeJSON(w, http.StatusAccepted, g)
	return fmt.Sprintf("succeed generation %d at %s", g.Gen, g.Commit), nil
}

// Generations reads viceroy/generations.jsonl.
func (v *Supervisor) Generations() ([]Generation, error) {
	f, err := os.Open(filepath.Join(v.dir(), "generations.jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Generation
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var g Generation
		if json.Unmarshal(sc.Bytes(), &g) == nil {
			out = append(out, g)
		}
	}
	return out, sc.Err()
}
