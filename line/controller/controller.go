// Package controller is the line's privileged actuator for workload requests.
// It holds the engine; `line serve` keeps identity, policy and records
// (design-docs/line-controller.md). Each pass compares the workloads serve
// wants with the engine objects carrying this line's labels and converges
// them. The controller keeps no state of its own, so a restart is safe.
package controller

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Fewbytes/sweatshop/line/station"
)

// DefaultGrace is how long a finished or unrequested engine object is kept
// before it is removed.
const DefaultGrace = 10 * time.Minute

// logTail is how much of a workload's log the controller reports.
const logTail = 32 << 10

// Instance is one workload as the engine creates it. The engine adds the
// hard limits (no privileges, no capabilities, non-root, the line's network).
type Instance struct {
	Workload string // the request ID, carried as a label
	Image    string
	Args     []string
	Env      map[string]string
	Memory   string
	CPUs     string
	Pids     int
}

// Observed is an engine object carrying this line's labels.
type Observed struct {
	Workload string // the request ID from its label; "" if the label is absent
	Running  bool
	ExitCode int       // meaningful when !Running
	Since    time.Time // creation while running, finish time once exited
}

// Engine is the controller's view of a container engine. Implementations
// touch only objects labelled with their own line.
type Engine interface {
	// Create starts the instance. It fails if one already exists for the workload.
	Create(ctx context.Context, in Instance) error
	List(ctx context.Context) ([]Observed, error)
	// Logs returns the tail of the workload's output.
	Logs(ctx context.Context, workload string) (string, error)
	// Remove deletes the workload's object; one already gone is not an error.
	Remove(ctx context.Context, workload string) error
}

// API is the controller's view of `line serve`.
type API interface {
	ControllerPending(ctx context.Context) ([]station.Workload, error)
	ControllerReport(ctx context.Context, rep station.WorkloadStatus) error
}

// Controller reconciles serve's workloads against the engine.
type Controller struct {
	API    API
	Engine Engine
	Grace  time.Duration // default DefaultGrace
	Now    func() time.Time
	Poll   time.Duration // default 2s
	Log    func(format string, args ...any)
}

func (c *Controller) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(format, args...)
	}
}

func (c *Controller) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Controller) grace() time.Duration {
	if c.Grace > 0 {
		return c.Grace
	}
	return DefaultGrace
}

// Run reconciles until ctx ends.
func (c *Controller) Run(ctx context.Context) error {
	poll := c.Poll
	if poll <= 0 {
		poll = 2 * time.Second
	}
	for {
		if err := c.Once(ctx); err != nil && ctx.Err() == nil {
			c.logf("controller: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// Once runs one reconcile pass. A failure on one workload does not stop the
// others; the errors are joined.
func (c *Controller) Once(ctx context.Context) error {
	wanted, err := c.API.ControllerPending(ctx)
	if err != nil {
		return fmt.Errorf("pending: %w", err)
	}
	objects, err := c.Engine.List(ctx)
	if err != nil {
		return fmt.Errorf("list engine objects: %w", err)
	}
	byID := make(map[string]Observed, len(objects))
	for _, o := range objects {
		byID[o.Workload] = o
	}
	var errs []error
	for _, w := range wanted {
		obj, has := byID[w.ID]
		delete(byID, w.ID)
		if err := c.reconcile(ctx, w, obj, has); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", w.ID, err))
		}
	}
	// What is left has no live request: a removed one, one serve forgot, or an
	// object with a missing label. The grace period spares objects whose
	// request serve has not recorded yet.
	for id, obj := range byID {
		if obj.Workload == "" || c.now().Sub(obj.Since) < c.grace() {
			continue
		}
		c.logf("controller: removing orphan %s", id)
		if err := c.Engine.Remove(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("remove orphan %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// unsupported names what the podman backend cannot do yet, so a request for
// it fails visibly instead of running without it.
func unsupported(sp station.WorkloadSpec) string {
	switch {
	case sp.Build != nil:
		return "build requests are not supported by the podman backend"
	case len(sp.Secrets) > 0:
		return "secrets are not supported by the podman backend"
	case len(sp.Volumes) > 0:
		return "volumes are not supported by the podman backend"
	case sp.Expose != nil:
		return "expose is not supported by the podman backend"
	}
	return ""
}

func (c *Controller) reconcile(ctx context.Context, w station.Workload, obj Observed, has bool) error {
	if w.Stopped || w.State == station.WorkloadExpired {
		detail := "stopped by its owner"
		if w.State == station.WorkloadExpired {
			detail = "ttl passed"
		}
		return c.retire(ctx, w, has, detail)
	}
	switch {
	case !has:
		if w.State != station.WorkloadApproved {
			return nil // finished earlier and cleaned up, or failed to start
		}
		return c.create(ctx, w)
	case obj.Running:
		if w.State == station.WorkloadRunning {
			return nil
		}
		return c.report(ctx, w, station.WorkloadRunning, "", false)
	}
	// Exited. Report once, keep the object for its logs, then clean it up.
	state, detail := station.WorkloadExited, "exit code "+strconv.Itoa(obj.ExitCode)
	if obj.ExitCode != 0 {
		state = station.WorkloadFailed
	}
	if w.State != state {
		return c.report(ctx, w, state, detail, true)
	}
	if c.now().Sub(obj.Since) < c.grace() {
		return nil
	}
	return c.retire(ctx, w, true, "finished; kept for "+c.grace().String())
}

func (c *Controller) create(ctx context.Context, w station.Workload) error {
	sp := w.Spec
	if why := unsupported(sp); why != "" {
		return c.report(ctx, w, station.WorkloadFailed, why, false)
	}
	in := Instance{Workload: w.ID, Image: sp.Image, Args: sp.Args, Env: sp.Env,
		Memory: sp.Resources.Memory, CPUs: sp.Resources.CPUs, Pids: sp.Resources.Pids}
	if err := c.Engine.Create(ctx, in); err != nil {
		// The error text comes from the engine and names no secret: the spec
		// carries none.
		return errors.Join(err, c.report(ctx, w, station.WorkloadFailed, "create: "+err.Error(), false))
	}
	c.logf("controller: started %s for %s", w.ID, w.Station)
	return c.report(ctx, w, station.WorkloadRunning, "", false)
}

// retire removes the workload's engine object, if any, and tells serve.
func (c *Controller) retire(ctx context.Context, w station.Workload, has bool, detail string) error {
	var logs string
	if has {
		// Logs are best effort: the object is going away either way.
		logs, _ = c.Engine.Logs(ctx, w.ID)
		if err := c.Engine.Remove(ctx, w.ID); err != nil {
			return fmt.Errorf("remove: %w", err)
		}
	}
	return c.reportWith(ctx, w, station.WorkloadRemoved, detail, logs)
}

func (c *Controller) report(ctx context.Context, w station.Workload, state, detail string, withLogs bool) error {
	var logs string
	if withLogs {
		logs, _ = c.Engine.Logs(ctx, w.ID)
	}
	return c.reportWith(ctx, w, state, detail, logs)
}

func (c *Controller) reportWith(ctx context.Context, w station.Workload, state, detail, logs string) error {
	return c.API.ControllerReport(ctx, station.WorkloadStatus{
		ID: w.ID, State: state, Detail: detail, Logs: logs[max(0, len(logs)-logTail):],
	})
}
