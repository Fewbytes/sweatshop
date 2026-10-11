package runner

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// Fake is an in-memory Runner for tests. Containers exit with ExitCode as
// soon as they start, unless Hold is set.
type Fake struct {
	ExitCode int
	Hold     bool // containers stay running until Finish
	Output   string

	mu      sync.Mutex
	started map[string]Spec
	done    map[string]chan struct{}
	Calls   []string
}

func (f *Fake) note(format string, args ...any) {
	f.mu.Lock()
	f.Calls = append(f.Calls, fmt.Sprintf(format, args...))
	f.mu.Unlock()
}

func (f *Fake) Build(_ context.Context, _, _, tag string) (string, error) {
	f.note("build %s", tag)
	return tag, nil
}

func (f *Fake) Pull(_ context.Context, ref string) (string, error) {
	f.note("pull %s", ref)
	return ref, nil
}

func (f *Fake) Start(_ context.Context, spec Spec) error {
	f.note("start %s", spec.Name)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.started == nil {
		f.started, f.done = map[string]Spec{}, map[string]chan struct{}{}
	}
	if _, ok := f.started[spec.Name]; ok {
		return fmt.Errorf("container %s exists", spec.Name)
	}
	f.started[spec.Name] = spec
	ch := make(chan struct{})
	f.done[spec.Name] = ch
	if !f.Hold {
		close(ch)
	}
	return nil
}

// Spec returns the spec container name was started with.
func (f *Fake) Spec(name string) (Spec, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.started[name]
	return s, ok
}

// Finish ends a held container.
func (f *Fake) Finish(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ch := f.done[name]; ch != nil {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

func (f *Fake) chanFor(name string) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.done[name]
}

func (f *Fake) Wait(ctx context.Context, name string) (int, error) {
	ch := f.chanFor(name)
	if ch == nil {
		return -1, fmt.Errorf("no container %s", name)
	}
	select {
	case <-ch:
		return f.ExitCode, nil
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}

func (f *Fake) Logs(_ context.Context, name string, w io.Writer) error {
	if f.chanFor(name) == nil {
		return fmt.Errorf("no container %s", name)
	}
	_, err := io.WriteString(w, f.Output)
	return err
}

func (f *Fake) Remove(_ context.Context, name string) error {
	f.note("remove %s", name)
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.started, name)
	delete(f.done, name)
	return nil
}

func (f *Fake) Inspect(_ context.Context, name string) (State, error) {
	ch := f.chanFor(name)
	if ch == nil {
		return State{}, nil
	}
	select {
	case <-ch:
		return State{Exists: true, ExitCode: f.ExitCode}, nil
	default:
		return State{Exists: true, Running: true}, nil
	}
}

var _ Runner = (*Fake)(nil)
var _ Runner = (*CLI)(nil)
