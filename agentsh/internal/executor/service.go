package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/service"
)

type ServiceState string

const (
	ServiceStateRunning ServiceState = "running"
	ServiceStateStopped ServiceState = "stopped"
	ServiceStateCrashed ServiceState = "crashed"
)

// Service is a named background invocation tracked across daemon restarts.
// Unlike a plain background Bash invocation, it has a stable name a caller
// can use to check on or kill it later without holding onto an invocation
// ID. There is no auto-restart loop: crash detection is passive, surfaced
// only when something asks for status.
type Service struct {
	Name         string       `json:"name"`
	InvocationID string       `json:"invocation_id"`
	Command      string       `json:"command"`
	Session      string       `json:"session"`
	CWD          string       `json:"cwd"`
	PID          int          `json:"pid,omitempty"`
	StartedAt    time.Time    `json:"started_at"`
	State        ServiceState `json:"state"`
	ExitCode     *int         `json:"exit_code,omitempty"`

	// StoppedByUser distinguishes an intentional KillService from a process
	// that just disappeared; it decides whether a service found dead reads
	// back as "stopped" or "crashed".
	StoppedByUser bool `json:"stopped_by_user,omitempty"`
}

type ServiceRequest struct {
	Name    string
	Command string
	Session string
	CWD     string
	Timeout time.Duration

	// Readiness, if it has any predicates set, makes StartService block
	// until they all pass or Readiness.Timeout expires. The service is
	// already running (and returned) either way — a readiness failure
	// doesn't kill it, it just reports that it never became ready.
	Readiness service.ReadinessSpec
}

var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func validateServiceName(name string) error {
	if !serviceNamePattern.MatchString(name) {
		return fmt.Errorf("invalid service name %q: must match %s", name, serviceNamePattern.String())
	}
	return nil
}

// serviceSupport is embedded state for Executor's service surface, kept in
// its own file/mutex since it's logically independent of invocation
// tracking (e.running) even though StartService drives Execute underneath.
type serviceSupport struct {
	svcMu sync.Mutex
	// services is populated lazily; nil until first use so an Executor
	// built without service support (most existing tests) pays nothing.
	services map[string]*Service

	// ServicesDir is where service records are persisted as JSON, one file
	// per service. Persistence is best-effort and skipped entirely when
	// empty — set it (daemon does, to paths.Services) to survive restarts.
	ServicesDir string
}

func (e *Executor) serviceMap() map[string]*Service {
	if e.services == nil {
		e.services = make(map[string]*Service)
	}
	return e.services
}

// StartService runs command as a named background invocation. A second
// StartService under the same name fails while the first is still actually
// running (checked against live invocation state, not a possibly-stale
// persisted record) but succeeds once it has stopped or crashed.
func (e *Executor) StartService(ctx context.Context, req ServiceRequest) (Service, error) {
	if err := validateServiceName(req.Name); err != nil {
		return Service{}, err
	}
	if req.Session == "" {
		req.Session = "default"
	}

	e.svcMu.Lock()
	existing, ok := e.serviceMap()[req.Name]
	e.svcMu.Unlock()
	if ok && e.isServiceRunning(existing) {
		return Service{}, fmt.Errorf("service %q is already running", req.Name)
	}

	invocation, err := e.Execute(ctx, Request{
		Command: req.Command, Session: req.Session, CWD: req.CWD,
		Timeout: req.Timeout, Background: true,
	})
	if err != nil {
		return Service{}, err
	}
	pid := 0
	if invocation.PID != nil {
		pid = *invocation.PID
	}
	svc := Service{
		Name: req.Name, InvocationID: invocation.ID, Command: req.Command,
		Session: invocation.Session, CWD: invocation.CWD, PID: pid,
		StartedAt: invocation.StartedAt, State: ServiceStateRunning,
	}

	e.svcMu.Lock()
	e.serviceMap()[req.Name] = &svc
	e.svcMu.Unlock()

	if err := e.persistService(svc); err != nil {
		return svc, fmt.Errorf("start service: persist record: %w", err)
	}

	predicates, err := service.BuildPredicates(req.Readiness, e.serviceStdoutTail(invocation.ID, req.Readiness.TailBytes))
	if err != nil {
		return svc, err
	}
	if len(predicates) > 0 {
		if err := service.WaitReady(ctx, predicates, req.Readiness.Timeout, req.Readiness.PollInterval); err != nil {
			return svc, fmt.Errorf("service %q did not become ready: %w", req.Name, err)
		}
	}
	return svc, nil
}

// serviceStdoutTail returns a func reading the last tailBytes of a running
// invocation's stdout, for a StdoutRegexPredicate. It opens a fresh
// snapshot on every call — readiness polls at ~250ms intervals, so this is
// not hot enough to warrant keeping a reader open across calls.
func (e *Executor) serviceStdoutTail(invocationID string, tailBytes int) func() ([]byte, error) {
	if tailBytes <= 0 {
		tailBytes = service.DefaultTailBytes
	}
	return func() ([]byte, error) {
		reader, running, err := e.OpenRunning(invocationID, "stdout")
		if err != nil {
			return nil, err
		}
		if !running || reader == nil {
			return nil, fmt.Errorf("invocation %q is not running", invocationID)
		}
		defer reader.Close()
		file, ok := reader.(*os.File)
		if !ok {
			return io.ReadAll(reader)
		}
		info, err := file.Stat()
		if err != nil {
			return nil, err
		}
		start := int64(0)
		if size := info.Size(); size > int64(tailBytes) {
			start = size - int64(tailBytes)
		}
		if _, err := file.Seek(start, io.SeekStart); err != nil {
			return nil, err
		}
		return io.ReadAll(file)
	}
}

// isServiceRunning asks the live invocation table rather than trusting
// svc.State, which self-heals a stale "running" record left over from a
// crash without needing an explicit status check first.
func (e *Executor) isServiceRunning(svc *Service) bool {
	e.mu.Lock()
	_, running := e.running[svc.InvocationID]
	e.mu.Unlock()
	return running
}

// ServiceStatus reports a named service's current state, reconciling a
// stale "running" record against reality: if the invocation is no longer
// tracked as running, it reads back as "crashed" (or "stopped" if
// KillService caused it) and the record is updated in place.
func (e *Executor) ServiceStatus(ctx context.Context, name string) (Service, error) {
	e.svcMu.Lock()
	svc, ok := e.serviceMap()[name]
	if !ok {
		e.svcMu.Unlock()
		return Service{}, fmt.Errorf("unknown service %q", name)
	}
	current := *svc
	e.svcMu.Unlock()

	if current.State != ServiceStateRunning || e.isServiceRunning(&current) {
		return current, nil
	}

	updated := current
	updated.State = ServiceStateCrashed
	if current.StoppedByUser {
		updated.State = ServiceStateStopped
	}
	if e.Store != nil {
		if inv, err := e.Store.GetInvocation(ctx, current.InvocationID); err == nil {
			updated.ExitCode = inv.ExitCode
		}
	}

	e.svcMu.Lock()
	e.serviceMap()[name] = &updated
	e.svcMu.Unlock()
	_ = e.persistService(updated)
	return updated, nil
}

// KillService signals a running service and removes its record — unlike a
// plain BashKill, the name is freed immediately for reuse rather than left
// behind as a "stopped" record (that state is reserved for services found
// dead passively, e.g. after a daemon restart).
func (e *Executor) KillService(name string, sig syscall.Signal) error {
	e.svcMu.Lock()
	svc, ok := e.serviceMap()[name]
	if !ok {
		e.svcMu.Unlock()
		return fmt.Errorf("unknown service %q", name)
	}
	svc.StoppedByUser = true
	delete(e.serviceMap(), name)
	e.svcMu.Unlock()

	var killErr error
	if e.isServiceRunning(svc) {
		killErr = e.Kill(svc.InvocationID, sig)
	}
	if err := e.removeServiceFile(name); err != nil && killErr == nil {
		killErr = err
	}
	return killErr
}

// LoadServices reconciles persisted service records at daemon startup: a
// record still marked "running" whose PID isn't alive gets marked "stopped"
// (the record survives — only the process is gone — per the same shape as
// storage.Reconcile for invocations).
func (e *Executor) LoadServices(servicesDir string, alive func(int) bool) error {
	e.ServicesDir = servicesDir
	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	e.svcMu.Lock()
	defer e.svcMu.Unlock()
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(servicesDir, entry.Name()))
		if err != nil {
			continue
		}
		var svc Service
		if err := json.Unmarshal(data, &svc); err != nil || svc.Name == "" {
			continue
		}
		if svc.State == ServiceStateRunning && (svc.PID <= 0 || !alive(svc.PID)) {
			svc.State = ServiceStateStopped
			data, err := json.Marshal(svc)
			if err == nil {
				_ = os.WriteFile(filepath.Join(servicesDir, svc.Name+".json"), data, 0o600)
			}
		}
		e.serviceMap()[svc.Name] = &svc
	}
	return nil
}

func (e *Executor) persistService(svc Service) error {
	if e.ServicesDir == "" {
		return nil
	}
	if err := os.MkdirAll(e.ServicesDir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(svc)
	if err != nil {
		return err
	}
	tmp := filepath.Join(e.ServicesDir, "."+svc.Name+".json.tmp")
	final := filepath.Join(e.ServicesDir, svc.Name+".json")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

func (e *Executor) removeServiceFile(name string) error {
	if e.ServicesDir == "" {
		return nil
	}
	err := os.Remove(filepath.Join(e.ServicesDir, name+".json"))
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
