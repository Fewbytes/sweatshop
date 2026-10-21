package runner

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sync"
	"time"

	"github.com/Fewbytes/sweatshop/line/station"
)

// WorkloadAPI is the part of the station API that Remote uses; station.Client
// implements it.
type WorkloadAPI interface {
	WorkloadRequest(ctx context.Context, spec station.WorkloadSpec) (station.Workload, error)
	WorkloadGet(ctx context.Context, id string) (station.Workload, error)
	WorkloadStop(ctx context.Context, id string) (station.Workload, error)
	WorkloadLogs(ctx context.Context, id string) (string, error)
}

// defaultRemotePoll is how often Remote.Wait re-reads a workload's state.
const defaultRemotePoll = 2 * time.Second

// pinnedRef matches an image reference pinned by digest, the only kind a
// workload request accepts.
var pinnedRef = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)

// Remote runs containers as workload requests to serve, so a station needs
// neither a container engine nor its socket. The controller runs them.
//
// The request carries what the protocol has: the image, args, env, resources
// and named volumes. It has no container name, no mount target and no secret
// values; the controller chooses the first two, and Start refuses a Spec with
// secrets rather than drop them.
type Remote struct {
	API  WorkloadAPI
	Kind string // workload kind to request, e.g. "test"; policy is per kind
	// Poll is the interval between state reads in Wait (default 2s). After
	// returns a channel that fires after d (default time.After); tests inject it.
	Poll  time.Duration
	After func(d time.Duration) <-chan time.Time

	mu  sync.Mutex
	ids map[string]string // runner container name -> workload ID
}

var (
	_ Runner      = (*Remote)(nil)
	_ WorkloadAPI = station.Client{}
)

func (r *Remote) workloadID(name string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.ids[name]
	if !ok {
		return "", fmt.Errorf("no workload started as %q", name)
	}
	return id, nil
}

// Build is not available: build requests are not implemented by the controller yet.
func (r *Remote) Build(context.Context, string, string, string) (string, error) {
	return "", fmt.Errorf("remote runner cannot build images yet")
}

// Pull accepts only a digest-pinned reference, which it returns as is: the
// controller's engine pulls it when the workload starts.
func (r *Remote) Pull(_ context.Context, ref string) (string, error) {
	if !pinnedRef.MatchString(ref) {
		return "", fmt.Errorf("remote runner needs an image pinned by digest, got %q", ref)
	}
	return ref, nil
}

// Start requests a workload for spec and returns once serve has recorded it.
// A request the policy denies is an error. One waiting for the king's approval
// is started as far as the caller is concerned; Wait blocks until it finishes.
func (r *Remote) Start(ctx context.Context, spec Spec) error {
	if len(spec.Secrets) > 0 {
		return fmt.Errorf("remote runner cannot carry secret values; %s asks for %d", spec.Name, len(spec.Secrets))
	}
	req := station.WorkloadSpec{
		Kind: r.Kind, Image: spec.Image, Args: spec.Args, Env: spec.Env,
		Resources: station.WorkloadResources{Memory: spec.Memory, CPUs: spec.CPUs, Pids: spec.Pids},
		Purpose:   "runner container " + spec.Name,
	}
	for _, m := range spec.Mounts {
		if !m.ReadOnly {
			return fmt.Errorf("volume %q: workloads mount volumes read-only", m.Volume)
		}
		req.Volumes = append(req.Volumes, m.Volume)
	}
	r.mu.Lock()
	if _, ok := r.ids[spec.Name]; ok {
		r.mu.Unlock()
		return fmt.Errorf("container %s exists", spec.Name)
	}
	r.mu.Unlock()
	w, err := r.API.WorkloadRequest(ctx, req)
	if err != nil {
		return fmt.Errorf("request workload for %s: %w", spec.Name, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ids == nil {
		r.ids = map[string]string{}
	}
	r.ids[spec.Name] = w.ID
	return nil
}

// finished reports whether the workload will not change state again.
func finished(w station.Workload) bool {
	switch w.State {
	case station.WorkloadExited, station.WorkloadFailed, station.WorkloadDenied,
		station.WorkloadExpired, station.WorkloadRemoved:
		return true
	}
	return false
}

// Wait polls the workload until it finishes and returns the container's exit
// code. A workload that ended without one (denied, expired, removed, or the
// engine failed to create it) is an error carrying serve's reason.
func (r *Remote) Wait(ctx context.Context, name string) (int, error) {
	id, err := r.workloadID(name)
	if err != nil {
		return -1, err
	}
	poll, after := r.Poll, r.After
	if poll == 0 {
		poll = defaultRemotePoll
	}
	if after == nil {
		after = time.After
	}
	for {
		w, err := r.API.WorkloadGet(ctx, id)
		if err != nil {
			return -1, fmt.Errorf("read workload %s: %w", id, err)
		}
		if finished(w) {
			if code, ok := w.ExitCode(); ok {
				return code, nil
			}
			return -1, fmt.Errorf("workload %s %s: %s", id, w.State, w.Detail)
		}
		select {
		case <-after(poll):
		case <-ctx.Done():
			return -1, ctx.Err()
		}
	}
}

// Logs writes the latest log tail the controller reported, which is the whole
// output only once the container has finished.
func (r *Remote) Logs(ctx context.Context, name string, w io.Writer) error {
	id, err := r.workloadID(name)
	if err != nil {
		return err
	}
	logs, err := r.API.WorkloadLogs(ctx, id)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, logs)
	return err
}

// Remove asks serve to stop the workload and forgets the name; a name that was
// never started is not an error.
func (r *Remote) Remove(ctx context.Context, name string) error {
	r.mu.Lock()
	id, ok := r.ids[name]
	r.mu.Unlock()
	if !ok {
		return nil
	}
	if _, err := r.API.WorkloadStop(ctx, id); err != nil {
		return fmt.Errorf("stop workload %s: %w", id, err)
	}
	r.mu.Lock()
	delete(r.ids, name)
	r.mu.Unlock()
	return nil
}

// Inspect maps the workload's state: it exists until removed, and runs until
// it exits or fails.
func (r *Remote) Inspect(ctx context.Context, name string) (State, error) {
	r.mu.Lock()
	id, ok := r.ids[name]
	r.mu.Unlock()
	if !ok {
		return State{}, nil
	}
	w, err := r.API.WorkloadGet(ctx, id)
	if err != nil {
		return State{}, fmt.Errorf("read workload %s: %w", id, err)
	}
	if w.State == station.WorkloadRemoved || w.State == station.WorkloadDenied || w.State == station.WorkloadExpired {
		return State{}, nil
	}
	code, _ := w.ExitCode()
	return State{Exists: true, Running: !finished(w), ExitCode: code}, nil
}
