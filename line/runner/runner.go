// Package runner is the one interface the line uses to run station and
// viceroy containers, whatever runs them. Docker and podman ship here (CLI
// backends); k8s, nomad and ecs implement the same interface later.
//
// Nothing in the interface names a host path: mounts refer to named volumes
// and secrets are values. A backend decides what a volume is (a bind mount, a
// read-only k8s volume or git-daemon service) and where a secret lives (the
// CLI's environment, a k8s Secret, ...).
package runner

import (
	"context"
	"fmt"
	"io"
)

// VolumeLineRemote is the line's bare remote, mounted read-only.
const VolumeLineRemote = "line-remote"

// Mount attaches a named volume to the container.
type Mount struct {
	Volume   string // logical name, e.g. VolumeLineRemote
	Target   string // absolute path inside the container
	ReadOnly bool
}

// Spec describes one container.
type Spec struct {
	Name, Image  string // Image should be a digest reference where one exists
	Args         []string
	Env          map[string]string // not secret
	Secrets      map[string]string // environment variables kept out of argv and logs
	Mounts       []Mount
	Memory, CPUs string
	Pids         int
}

// State is what Inspect reports.
type State struct {
	Exists   bool
	Running  bool
	ExitCode int // meaningful once not Running
}

// Runner runs containers.
type Runner interface {
	// Build builds an image from the local contextDir and returns its reference.
	Build(ctx context.Context, contextDir, dockerfile, tag string) (string, error)
	// Pull fetches ref (preferably name@sha256:...) and returns the reference
	// to run, pinned by digest when the registry reports one.
	Pull(ctx context.Context, ref string) (string, error)
	// Start starts the container detached.
	Start(ctx context.Context, spec Spec) error
	// Wait blocks until the container exits and returns its exit code.
	Wait(ctx context.Context, name string) (int, error)
	// Logs copies the container's output to w.
	Logs(ctx context.Context, name string, w io.Writer) error
	// Remove force-removes the container; a missing one is not an error.
	Remove(ctx context.Context, name string) error
	// Inspect reports the container's state; a missing one has Exists false.
	Inspect(ctx context.Context, name string) (State, error)
}

// Volumes resolves volume names to backend-specific sources. For the CLI
// backends a source is a host directory.
type Volumes map[string]string

// New returns the backend for a constitution [runner] engine.
func New(engine string, volumes Volumes) (Runner, error) {
	switch engine {
	case "", "docker":
		return &CLI{Bin: "docker", Volumes: volumes}, nil
	case "podman":
		return &CLI{Bin: "podman", Volumes: volumes}, nil
	case "k8s", "nomad", "ecs":
		return nil, fmt.Errorf("runner engine %q is not implemented yet", engine)
	}
	return nil, fmt.Errorf("unknown runner engine %q", engine)
}
