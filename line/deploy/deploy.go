// Package deploy rolls a release onto the host. The line decides what to
// deploy and when (station/deploy.go); an Actuator applies the approved
// release through a Backend for the platform the line runs on. The Actuator
// is the only privileged piece: it holds no model or Discord credentials.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

// HealthWindow is how long a new release gets to report healthy.
const HealthWindow = 60 * time.Second

var (
	commitRe = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	imageRe  = regexp.MustCompile(`^[a-z0-9][a-zA-Z0-9._:/-]*$`)
)

// Release is an attested master commit and what was built from it. Container
// backends deploy Image@Digest; the launchd backend builds a binary from
// Commit and leaves Image and Digest empty.
type Release struct {
	Commit string `json:"commit"`
	Image  string `json:"image,omitempty"`  // repository, without tag or digest
	Digest string `json:"digest,omitempty"` // sha256:<64 hex>
}

// Ref is the digest-pinned image reference.
func (r Release) Ref() string { return r.Image + "@" + r.Digest }

// Validate checks the commit, and for container releases the digest pin.
// Tags are never enough: a tag can move after the king approved it.
func (r Release) Validate(container bool) error {
	if !commitRe.MatchString(r.Commit) {
		return fmt.Errorf("release commit %q is not a full lowercase hex object id", r.Commit)
	}
	if !container {
		return nil
	}
	if !imageRe.MatchString(r.Image) || strings.Contains(path.Base(r.Image), ":") {
		return fmt.Errorf("release image %q must be a repository without tag or digest", r.Image)
	}
	if !digestRe.MatchString(r.Digest) {
		return fmt.Errorf("release %s: image %s is not pinned by a sha256 digest", r.Commit, r.Image)
	}
	return nil
}

// Backend applies releases on one platform. It must not assume a local
// filesystem or a single host: Current reads the platform's own state so the
// Actuator can roll back without keeping any.
type Backend interface {
	// Current is the release the platform runs now (Commit may be empty when
	// it was not deployed by the line).
	Current(ctx context.Context) (Release, error)
	Apply(ctx context.Context, r Release) error
	// Rollback restores a release returned by Current.
	Rollback(ctx context.Context, prev Release) error
	// Health returns nil when the applied release serves.
	Health(ctx context.Context) error
}

// Status is the actuator's outcome for a release.
type Status struct {
	Commit string    `json:"commit"`
	State  string    `json:"state"` // StateApplied, StateRolledBack or StateFailed
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

const (
	StateApplied    = "applied"
	StateRolledBack = "rolled_back"
	StateFailed     = "failed" // the release failed and so did the rollback
)

// API is the actuator's view of the line.
type API interface {
	Desired(ctx context.Context) (*Release, error) // nil: nothing approved
	Report(ctx context.Context, s Status) error
}

// Actuator pulls the desired release and applies it.
type Actuator struct {
	API     API
	Backend Backend
	// Container requires releases to carry a digest-pinned image.
	Container bool
	Window    time.Duration // default HealthWindow
	Poll      time.Duration // health probe and desired poll interval, default 2s
	Log       func(format string, args ...any)

	// settled is the last commit this actuator finished with, whatever the
	// outcome, so a failed release is not retried until a new one is approved.
	settled string
}

func (a *Actuator) logf(format string, args ...any) {
	if a.Log != nil {
		a.Log(format, args...)
	}
}

func (a *Actuator) poll() time.Duration {
	if a.Poll > 0 {
		return a.Poll
	}
	return 2 * time.Second
}

// Run polls until ctx ends.
func (a *Actuator) Run(ctx context.Context) error {
	for {
		if err := a.Once(ctx); err != nil && ctx.Err() == nil {
			a.logf("deployer: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(a.poll()):
		}
	}
}

// Once handles the desired release, if it is new.
func (a *Actuator) Once(ctx context.Context) error {
	want, err := a.API.Desired(ctx)
	if err != nil {
		return fmt.Errorf("desired: %w", err)
	}
	if want == nil || want.Commit == a.settled {
		return nil
	}
	if err := want.Validate(a.Container); err != nil {
		a.settled = want.Commit
		return a.report(ctx, want.Commit, StateFailed, "refused: "+err.Error())
	}
	prev, err := a.Backend.Current(ctx)
	if err != nil {
		return fmt.Errorf("current release: %w", err)
	}
	if prev.Commit == want.Commit && (!a.Container || prev.Digest == want.Digest) {
		a.settled = want.Commit
		return nil // already running it
	}
	a.settled = want.Commit
	a.logf("deployer: applying %s (was %s)", want.Commit, prev.Commit)
	if err := a.Backend.Apply(ctx, *want); err != nil {
		return a.fail(ctx, *want, prev, "apply: "+err.Error())
	}
	if err := a.healthy(ctx); err != nil {
		return a.fail(ctx, *want, prev, "unhealthy: "+err.Error())
	}
	return a.report(ctx, want.Commit, StateApplied, "")
}

// healthy probes Health until it passes or the window closes.
func (a *Actuator) healthy(ctx context.Context) error {
	window := a.Window
	if window <= 0 {
		window = HealthWindow
	}
	deadline := time.Now().Add(window)
	for {
		err := a.Backend.Health(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !time.Now().Add(a.poll()).Before(deadline) {
			return fmt.Errorf("not healthy within %s: %w", window, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(a.poll()):
		}
	}
}

func (a *Actuator) fail(ctx context.Context, want, prev Release, why string) error {
	// A cancelled ctx must not stop the rollback: that is when it matters.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if err := a.Backend.Rollback(rctx, prev); err != nil {
		return errors.Join(a.report(ctx, want.Commit, StateFailed, why+"; rollback failed: "+err.Error()), err)
	}
	return a.report(ctx, want.Commit, StateRolledBack, why)
}

func (a *Actuator) report(ctx context.Context, commit, state, detail string) error {
	rctx := context.WithoutCancel(ctx)
	return a.API.Report(rctx, Status{Commit: commit, State: state, Detail: detail, At: time.Now().UTC()})
}

// HTTPHealth is a Health check: GET URL (the line's /v1/status) must be 200.
type HTTPHealth struct {
	URL, Token string
	Client     *http.Client
}

func (h HTTPHealth) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.URL, nil)
	if err != nil {
		return err
	}
	if h.Token != "" {
		req.Header.Set("Authorization", "Bearer "+h.Token)
	}
	c := h.Client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", h.URL, resp.Status)
	}
	return nil
}
