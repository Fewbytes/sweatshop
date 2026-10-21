package station

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Workload states (design-docs/line-controller.md, Request lifecycle). Serve
// sets pending, approved, denied and expired; the controller reports the rest.
const (
	WorkloadPending  = "pending"
	WorkloadApproved = "approved"
	WorkloadDenied   = "denied"
	WorkloadCreating = "creating"
	WorkloadRunning  = "running"
	WorkloadExited   = "exited"
	WorkloadFailed   = "failed"
	WorkloadExpired  = "expired"
	WorkloadRemoved  = "removed"
)

// WorkloadEvent is one audit record: a state change, who caused it and why.
type WorkloadEvent struct {
	At     time.Time `json:"at"`
	State  string    `json:"state"`
	By     string    `json:"by"` // a station name, "controller", "king:<message id>" or "serve"
	Detail string    `json:"detail,omitempty"`
}

// WorkloadBuild asks for an image built from a commit on the line remote.
type WorkloadBuild struct {
	Commit     string `json:"commit"`
	Dockerfile string `json:"dockerfile"`
}

// WorkloadResources bounds a workload; empty or zero fields take the rule's
// bound when it sets one.
type WorkloadResources struct {
	Memory string `json:"memory,omitempty"`
	CPUs   string `json:"cpus,omitempty"`
	Pids   int    `json:"pids,omitempty"`
}

// WorkloadExpose asks for a published port.
type WorkloadExpose struct {
	Port int    `json:"port"`
	Name string `json:"name"`
}

// WorkloadSpec is what a station asks for. The station's name and role are
// never part of it: they come from the token.
type WorkloadSpec struct {
	Kind      string            `json:"kind"`
	Image     string            `json:"image,omitempty"` // digest-pinned; exactly one of Image and Build
	Build     *WorkloadBuild    `json:"build,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Secrets   []string          `json:"secrets,omitempty"` // names only, resolved by the controller
	Volumes   []string          `json:"volumes,omitempty"` // named volumes only
	Resources WorkloadResources `json:"resources"`
	TTL       string            `json:"ttl,omitempty"` // Go duration, at most the rule's
	Expose    *WorkloadExpose   `json:"expose,omitempty"`
	Purpose   string            `json:"purpose,omitempty"`
}

// Workload is a request and its life. Spec holds the resolved values (rule
// defaults filled in), which the controller acts on.
type Workload struct {
	ID        string          `json:"id"`
	Station   string          `json:"station"`
	Role      string          `json:"role"`
	Rule      string          `json:"rule"` // "<role>/<kind>": the policy rule that covered the request
	Spec      WorkloadSpec    `json:"spec"`
	State     string          `json:"state"`
	Detail    string          `json:"detail,omitempty"`
	Stopped   bool            `json:"stopped,omitempty"` // the owner asked to stop it; the controller removes it
	ExpiresAt time.Time       `json:"expires_at,omitzero"`
	History   []WorkloadEvent `json:"history"`
}

// live reports whether the workload counts against its station's quota.
func (w Workload) live() bool {
	switch w.State {
	case WorkloadPending, WorkloadApproved, WorkloadCreating, WorkloadRunning:
		return !w.Stopped
	}
	return false
}

// controllerVisible reports whether the controller must still act on the
// workload: it has, or may have, an engine object.
func (w Workload) controllerVisible() bool {
	switch w.State {
	case WorkloadApproved, WorkloadCreating, WorkloadRunning, WorkloadExited, WorkloadFailed, WorkloadExpired:
		return true
	}
	return false
}

// WorkloadStore persists the line's workload requests as JSON, in the manner
// of DeployState. Now is the clock (nil: time.Now); IDs are sequential.
type WorkloadStore struct {
	Path string
	Now  func() time.Time

	mu sync.Mutex
}

type workloadFile struct {
	Next      int               `json:"next"`
	Workloads []Workload        `json:"workloads"`
	Logs      map[string]string `json:"logs,omitempty"` // by workload ID: the latest tail the controller reported
}

func (ws *WorkloadStore) now() time.Time {
	if ws.Now != nil {
		return ws.Now().UTC()
	}
	return time.Now().UTC()
}

func (ws *WorkloadStore) load() (workloadFile, error) {
	var f workloadFile
	data, err := os.ReadFile(ws.Path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	return f, json.Unmarshal(data, &f)
}

func (ws *WorkloadStore) save(f workloadFile) error {
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(ws.Path), 0o700); err != nil {
		return err
	}
	tmp := ws.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, ws.Path)
}

// expire moves workloads whose lifetime has passed to expired, recording it.
func (ws *WorkloadStore) expire(f *workloadFile) {
	now := ws.now()
	for i := range f.Workloads {
		w := &f.Workloads[i]
		switch w.State {
		case WorkloadApproved, WorkloadCreating, WorkloadRunning:
			if !w.ExpiresAt.IsZero() && !now.Before(w.ExpiresAt) {
				w.setState(now, WorkloadExpired, "serve", "ttl passed")
			}
		}
	}
}

func (w *Workload) setState(at time.Time, state, by, detail string) {
	w.State, w.Detail = state, detail
	w.History = append(w.History, WorkloadEvent{At: at, State: state, By: by, Detail: detail})
}

// update runs fn on the loaded, expiry-swept state under the lock and saves
// the result.
func (ws *WorkloadStore) update(fn func(*workloadFile) error) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	f, err := ws.load()
	if err != nil {
		return err
	}
	ws.expire(&f)
	if err := fn(&f); err != nil {
		return err
	}
	return ws.save(f)
}

// read returns the state with expiry applied in memory.
func (ws *WorkloadStore) read() (workloadFile, error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	f, err := ws.load()
	ws.expire(&f)
	return f, err
}

// find returns the index of the workload with id, or -1.
func (f *workloadFile) find(id string) int {
	for i, w := range f.Workloads {
		if w.ID == id {
			return i
		}
	}
	return -1
}

// add appends w with the next sequential ID and returns it.
func (f *workloadFile) add(w Workload) Workload {
	f.Next++
	w.ID = fmt.Sprintf("wl-%d", f.Next)
	f.Workloads = append(f.Workloads, w)
	return w
}
