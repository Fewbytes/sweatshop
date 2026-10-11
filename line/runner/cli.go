package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// HostGateway is how a container reaches the host on docker and podman.
const HostGateway = "host.containers.internal"

// CLI drives a docker-compatible CLI (docker, or rootless podman) without a
// shell. Env and secret values travel through the CLI's own environment,
// never its argv.
type CLI struct {
	Bin     string // "docker" (default) or "podman"
	Volumes Volumes
}

func (c *CLI) bin() string {
	if c.Bin == "" {
		return "docker"
	}
	return c.Bin
}

func (c *CLI) podman() bool { return filepath.Base(c.bin()) == "podman" }

func (c *CLI) run(ctx context.Context, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", c.bin(), args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (c *CLI) Build(ctx context.Context, contextDir, dockerfile, tag string) (string, error) {
	if strings.HasPrefix(tag, "-") {
		return "", fmt.Errorf("bad tag %q", tag)
	}
	_, err := c.run(ctx, nil, "build", "-f", dockerfile, "-t", tag, contextDir)
	return tag, err
}

func (c *CLI) Pull(ctx context.Context, ref string) (string, error) {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("bad image %q", ref)
	}
	if _, err := c.run(ctx, nil, "pull", ref); err != nil {
		return "", err
	}
	if strings.Contains(ref, "@sha256:") {
		return ref, nil
	}
	out, err := c.run(ctx, nil, "image", "inspect", "-f", "{{index .RepoDigests 0}}", ref)
	if err != nil {
		return ref, nil // no digest known; run by the reference given
	}
	return strings.TrimSpace(out), nil
}

// args builds the `run` argv and the environment carrying the values.
func (c *CLI) args(spec Spec) ([]string, []string, error) {
	if spec.Name == "" || strings.HasPrefix(spec.Name, "-") {
		return nil, nil, fmt.Errorf("bad container name %q", spec.Name)
	}
	if spec.Image == "" || strings.HasPrefix(spec.Image, "-") {
		return nil, nil, fmt.Errorf("bad image %q", spec.Image)
	}
	args := []string{"run", "-d", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--name", spec.Name}
	if !c.podman() {
		args = append(args, "--add-host="+HostGateway+":host-gateway")
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
	vars := map[string]string{}
	for _, m := range []map[string]string{spec.Env, spec.Secrets} {
		for k, v := range m {
			if k == "" || strings.ContainsAny(k, "=\x00") {
				return nil, nil, fmt.Errorf("bad environment variable name %q", k)
			}
			vars[k] = v
		}
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := os.Environ()
	for _, k := range keys {
		args = append(args, "-e", k)
		env = append(env, k+"="+vars[k])
	}
	for _, m := range spec.Mounts {
		src, ok := c.Volumes[m.Volume]
		if !ok {
			return nil, nil, fmt.Errorf("unknown volume %q", m.Volume)
		}
		if !filepath.IsAbs(src) || !filepath.IsAbs(m.Target) || strings.Contains(src+m.Target, ":") {
			return nil, nil, fmt.Errorf("bad mount %q -> %q", src, m.Target)
		}
		v := src + ":" + m.Target
		if m.ReadOnly {
			v += ":ro"
		}
		args = append(args, "-v", v)
	}
	return append(append(args, spec.Image), spec.Args...), env, nil
}

func (c *CLI) Start(ctx context.Context, spec Spec) error {
	args, env, err := c.args(spec)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, env, args...)
	return err
}

func (c *CLI) Wait(ctx context.Context, name string) (int, error) {
	out, err := c.run(ctx, nil, "wait", name)
	if err != nil {
		return -1, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

func (c *CLI) Logs(ctx context.Context, name string, w io.Writer) error {
	cmd := exec.CommandContext(ctx, c.bin(), "logs", name)
	cmd.Stdout, cmd.Stderr = w, w
	return cmd.Run()
}

func (c *CLI) Remove(ctx context.Context, name string) error {
	out, err := exec.CommandContext(ctx, c.bin(), "rm", "-f", "--", name).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "No such container") {
		return fmt.Errorf("%s rm: %w: %s", c.bin(), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (c *CLI) Inspect(ctx context.Context, name string) (State, error) {
	out, err := exec.CommandContext(ctx, c.bin(), "inspect", "-f", "{{.State.Running}} {{.State.ExitCode}}", "--", name).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return State{}, nil // no such container
		}
		return State{}, err
	}
	f := strings.Fields(string(out))
	if len(f) != 2 {
		return State{}, fmt.Errorf("%s inspect: unexpected output %q", c.bin(), out)
	}
	code, _ := strconv.Atoi(f[1])
	return State{Exists: true, Running: f[0] == "true", ExitCode: code}, nil
}
