package controller

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-units"
)

// Labels on every engine object the controller creates; they are how a pass
// finds what is its own without keeping state.
const (
	LabelLine     = "sweatshop.line"
	LabelWorkload = "sweatshop.workload"
)

// nonRootID is the nobody user and group id; a workload never runs as root.
const nonRootID int64 = 65534

// logTailLines is how many trailing log lines an engine returns.
const logTailLines int64 = 500

// Podman is the Engine backed by podman's Docker-compatible API socket
// (chosen over the podman bindings: lighter, builds with CGO_ENABLED=0, and
// one client also serves docker, as line.toml allows either engine).
type Podman struct {
	Client  *client.Client
	Line    string // the line's name; scopes every list and removal
	Network string // the line's own network; empty is the engine default
}

func (p Podman) containerName(workload string) string {
	return "sweatshop-" + p.Line + "-" + workload
}

func (p Podman) lineFilter() filters.Args {
	return filters.NewArgs(filters.Arg("label", LabelLine+"="+p.Line))
}

// Create pulls the image if the engine lacks it, then creates and starts the
// container with the hard limits of the design: no capabilities, no new
// privileges, non-root, no privileged mode, named networks only.
func (p Podman) Create(ctx context.Context, in Instance) error {
	host := &container.HostConfig{
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges"},
		NetworkMode: container.NetworkMode(p.Network),
	}
	if in.Memory != "" {
		mem, err := units.RAMInBytes(in.Memory)
		if err != nil {
			return fmt.Errorf("memory %q: %w", in.Memory, err)
		}
		host.Memory = mem
	}
	if in.CPUs != "" {
		cpus, err := strconv.ParseFloat(in.CPUs, 64)
		if err != nil || cpus <= 0 {
			return fmt.Errorf("cpus %q: want a positive number", in.CPUs)
		}
		host.NanoCPUs = int64(cpus * 1e9)
	}
	if in.Pids > 0 {
		pids := int64(in.Pids)
		host.PidsLimit = &pids
	}
	env := make([]string, 0, len(in.Env))
	for k, v := range in.Env {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	cfg := &container.Config{
		Image: in.Image, Cmd: in.Args, Env: env, User: fmt.Sprintf("%d:%d", nonRootID, nonRootID),
		Labels: map[string]string{LabelLine: p.Line, LabelWorkload: in.Workload},
	}
	if _, err := p.Client.ImageInspect(ctx, in.Image); errdefs.IsNotFound(err) {
		rc, err := p.Client.ImagePull(ctx, in.Image, image.PullOptions{})
		if err != nil {
			return fmt.Errorf("pull %s: %w", in.Image, err)
		}
		_, err = io.Copy(io.Discard, rc) // the pull finishes when the stream ends
		rc.Close()
		if err != nil {
			return fmt.Errorf("pull %s: %w", in.Image, err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect image %s: %w", in.Image, err)
	}
	created, err := p.Client.ContainerCreate(ctx, cfg, host, nil, nil, p.containerName(in.Workload))
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	if err := p.Client.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start container: %w", err)
	}
	return nil
}

// List returns the line's containers, running or not.
func (p Podman) List(ctx context.Context) ([]Observed, error) {
	list, err := p.Client.ContainerList(ctx, container.ListOptions{All: true, Filters: p.lineFilter()})
	if err != nil {
		return nil, err
	}
	out := make([]Observed, 0, len(list))
	for _, c := range list {
		info, err := p.Client.ContainerInspect(ctx, c.ID)
		if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", c.ID, err)
		}
		o := Observed{Workload: c.Labels[LabelWorkload], Running: info.State.Running}
		if o.Running {
			o.Since = time.Unix(c.Created, 0)
		} else {
			o.ExitCode = info.State.ExitCode
			if o.Since, err = time.Parse(time.RFC3339Nano, info.State.FinishedAt); err != nil {
				return nil, fmt.Errorf("container %s finished at %q: %w", c.ID, info.State.FinishedAt, err)
			}
		}
		out = append(out, o)
	}
	return out, nil
}

// Logs returns stdout then stderr of the workload's container, last lines only.
func (p Podman) Logs(ctx context.Context, workload string) (string, error) {
	rc, err := p.Client.ContainerLogs(ctx, p.containerName(workload), container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Tail: strconv.FormatInt(logTailLines, 10),
	})
	if err != nil {
		return "", err
	}
	defer rc.Close()
	var out bytes.Buffer
	// The engine multiplexes the two streams in frames (no TTY is allocated).
	if _, err := stdcopy.StdCopy(&out, &out, rc); err != nil {
		return "", err
	}
	return out.String(), nil
}

// Remove force-removes the workload's container.
func (p Podman) Remove(ctx context.Context, workload string) error {
	err := p.Client.ContainerRemove(ctx, p.containerName(workload), container.RemoveOptions{Force: true})
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}
