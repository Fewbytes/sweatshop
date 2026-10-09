// Package maker runs a Maker station: a host-side loop that takes a ready
// bead, lets an agent in a station container work it through the station
// API, and settles the bead by what actually landed on the line's master.
package maker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/linedir"
)

type Bead struct {
	ID, Title, Description string
	Priority               int
	Labels                 []string
}

type Beads interface {
	Ready(ctx context.Context, label string) ([]Bead, error)
	Claim(ctx context.Context, id, agent string) error
	Close(ctx context.Context, id, reason string) error
	Release(ctx context.Context, id, note string) error
}

type Engine interface {
	// Run starts the station container and waits for it; returns exit code.
	Run(ctx context.Context, spec ContainerSpec) (int, error)
}

type Mount struct {
	Host, Container string
	ReadOnly        bool
}

type ContainerSpec struct {
	Image, Name string
	Env         map[string]string
	Mounts      []Mount
	Args        []string
}

type Runner struct {
	Line    linedir.Line
	Station linedir.Station
	Beads   Beads
	Engine  Engine
	URL     string            // as seen from inside the container; default http://host.containers.internal:<port>
	Creds   map[string]string // model credentials passed through to the container
	Log     *log.Logger
}

// credentialVars are the host variables a station container may inherit.
var credentialVars = []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}

// HostCreds collects the model credentials set in the host environment.
func HostCreds(lookup func(string) (string, bool)) map[string]string {
	creds := map[string]string{}
	for _, k := range credentialVars {
		if v, set := lookup(k); set && v != "" {
			creds[k] = v
		}
	}
	return creds
}

// Pick chooses the next card: andon cards first (the line is stopped until
// one lands), then the lowest priority number, then ID for a stable order.
func Pick(beads []Bead) (Bead, bool) {
	if len(beads) == 0 {
		return Bead{}, false
	}
	sorted := slices.Clone(beads)
	sort.SliceStable(sorted, func(i, j int) bool {
		ai, aj := slices.Contains(sorted[i].Labels, "andon"), slices.Contains(sorted[j].Labels, "andon")
		if ai != aj {
			return ai
		}
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority < sorted[j].Priority
		}
		return sorted[i].ID < sorted[j].ID
	})
	return sorted[0], true
}

// landedDepth bounds how far back Landed looks; a run's commit lands at the
// tip, so only a very busy line could bury it this deep before we look.
const landedDepth = "200"

// Landed reports whether a commit on remote's master carries the trailer
// `Bead: <id>`. The line is the source of truth: the agent's own report of
// success is never trusted.
func Landed(ctx context.Context, remote gitx.Repo, beadID string) (bool, error) {
	// Records end in \x1e and fields in \x00 so multi-line trailers parse.
	out, err := remote.Git(ctx, "log", "--format=%H%x00%(trailers:key=Bead,valueonly,separator=%x00)%x1e",
		"-n", landedDepth, "refs/heads/master", "--")
	if err != nil {
		return false, err
	}
	for _, rec := range strings.Split(out, "\x1e") {
		fields := strings.Split(strings.TrimSpace(rec), "\x00")
		for _, v := range fields[1:] {
			if strings.TrimSpace(v) == beadID {
				return true, nil
			}
		}
	}
	return false, nil
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log.Printf(format, args...)
	}
}

var unsafeNameChars = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// Once works one card: pick → claim → run → settle. It reports whether there
// was a card to work.
func (r *Runner) Once(ctx context.Context) (bool, error) {
	ready, err := r.Beads.Ready(ctx, "line")
	if err != nil {
		return false, fmt.Errorf("ready beads: %w", err)
	}
	b, found := Pick(ready)
	if !found {
		return false, nil
	}
	token, err := r.Station.Token()
	if err != nil {
		return false, fmt.Errorf("station %s token: %w", r.Station.Name, err)
	}
	if err := r.Beads.Claim(ctx, b.ID, r.Station.AgentID); err != nil {
		return false, fmt.Errorf("claim %s: %w", b.ID, err)
	}
	r.logf("claimed %s: %s", b.ID, b.Title)

	url := r.URL
	if url == "" {
		url = fmt.Sprintf("http://host.containers.internal:%d", r.Line.Port)
	}
	env := map[string]string{}
	for k, v := range r.Creds {
		env[k] = v
	}
	env["SWEATSHOP_LINE_URL"] = url
	env["SWEATSHOP_LINE_TOKEN"] = token
	env["SWEATSHOP_BEAD"] = b.ID
	env["SWEATSHOP_AGENT_ID"] = r.Station.AgentID
	env["SWEATSHOP_PROMPT"] = Prompt(b, AndonEvent(b))
	spec := ContainerSpec{
		Image:  r.Line.Image,
		Name:   unsafeNameChars.ReplaceAllString("sweatshop-"+r.Station.Name+"-"+b.ID, "_"),
		Env:    env,
		Mounts: []Mount{{Host: r.Line.Remote, Container: "/line/remote.git", ReadOnly: true}},
	}
	code, runErr := r.Engine.Run(ctx, spec)
	r.logf("%s: container exited %d (err %v)", b.ID, code, runErr)

	// Settle even when interrupted, so a stopped runner does not leave the
	// card claimed by nobody who is working it.
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	landed, err := Landed(settle, gitx.Repo{Dir: r.Line.Remote}, b.ID)
	if err != nil {
		// Leave the claim: releasing a card that may have landed invites a
		// duplicate, and an operator can see the in-progress bead.
		return true, errors.Join(runErr, fmt.Errorf("%s: read line master: %w", b.ID, err))
	}
	if landed {
		r.logf("%s landed", b.ID)
		return true, errors.Join(runErr, r.Beads.Close(settle, b.ID, "landed via line"))
	}
	note := fmt.Sprintf("maker run ended without landing (exit %d)", code)
	r.logf("%s: %s", b.ID, note)
	return true, errors.Join(runErr, r.Beads.Release(settle, b.ID, note))
}

// Loop runs Once until ctx ends, sleeping idle between empty polls and after
// errors so a broken setup does not spin.
func (r *Runner) Loop(ctx context.Context, idle time.Duration) error {
	for {
		worked, err := r.Once(ctx)
		if err != nil {
			r.logf("once: %v", err)
		}
		if ctx.Err() != nil {
			return nil
		}
		if worked && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(idle):
		}
	}
}

// BeadsCLI drives the bd CLI in Dir. Actor names the station in bd's audit
// trail; bd only lets the claim holder release a claim, so it must match the
// agent passed to Claim.
type BeadsCLI struct{ Dir, Actor string }

func (b BeadsCLI) bd(ctx context.Context, args ...string) ([]byte, error) {
	if b.Actor != "" {
		args = append(args, "--actor", b.Actor)
	}
	cmd := exec.CommandContext(ctx, "bd", args...)
	cmd.Dir = b.Dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bd %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func checkID(id string) error {
	if id == "" || strings.HasPrefix(id, "-") {
		return fmt.Errorf("bad bead id %q", id)
	}
	return nil
}

func (b BeadsCLI) Ready(ctx context.Context, label string) ([]Bead, error) {
	out, err := BeadsCLI{Dir: b.Dir}.bd(ctx, "ready", "--label", label, "--limit", "0", "--json")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Priority    int      `json:"priority"`
		Labels      []string `json:"labels"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("bd ready: parse output: %w", err)
	}
	beads := make([]Bead, 0, len(rows))
	for _, r := range rows {
		beads = append(beads, Bead{ID: r.ID, Title: r.Title, Description: r.Description, Priority: r.Priority, Labels: r.Labels})
	}
	return beads, nil
}

// Claim sets the assignee to agent and the status to in_progress; bd's
// --claim takes the assignee from the actor.
func (b BeadsCLI) Claim(ctx context.Context, id, agent string) error {
	if err := checkID(id); err != nil {
		return err
	}
	_, err := BeadsCLI{Dir: b.Dir, Actor: agent}.bd(ctx, "update", id, "--claim")
	return err
}

func (b BeadsCLI) Close(ctx context.Context, id, reason string) error {
	if err := checkID(id); err != nil {
		return err
	}
	_, err := b.bd(ctx, "close", id, "--reason", reason)
	return err
}

// Release reopens the card for another run (clearing the assignee) and
// leaves the note as a comment so the next worker sees what happened.
func (b BeadsCLI) Release(ctx context.Context, id, note string) error {
	if err := checkID(id); err != nil {
		return err
	}
	if _, err := b.bd(ctx, "unclaim", id, "--reason", note); err != nil {
		return err
	}
	_, err := b.bd(ctx, "comments", "add", id, note)
	return err
}

// DockerEngine runs station containers with a docker-compatible CLI.
type DockerEngine struct {
	Bin            string // default "docker"
	Stdout, Stderr io.Writer
}

// Run execs `<bin> run --rm ...` without a shell. Environment values reach
// the engine through its own environment (`-e K` takes the value from there)
// so the station token never appears in the host's process table.
func (d DockerEngine) Run(ctx context.Context, spec ContainerSpec) (int, error) {
	bin := d.Bin
	if bin == "" {
		bin = "docker"
	}
	args := []string{"run", "--rm"}
	if spec.Name != "" {
		args = append(args, "--name", spec.Name)
	}
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			return -1, fmt.Errorf("bad environment variable name %q", k)
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
		if strings.Contains(m.Host, ":") || strings.Contains(m.Container, ":") || m.Host == "" || m.Container == "" {
			return -1, fmt.Errorf("bad mount %q -> %q", m.Host, m.Container)
		}
		v := m.Host + ":" + m.Container
		if m.ReadOnly {
			v += ":ro"
		}
		args = append(args, "-v", v)
	}
	if spec.Image == "" || strings.HasPrefix(spec.Image, "-") {
		return -1, fmt.Errorf("bad image %q", spec.Image)
	}
	args = append(append(args, spec.Image), spec.Args...)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = d.Stdout, d.Stderr
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stderr
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}
