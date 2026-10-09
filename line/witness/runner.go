package witness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/line/constitution"
)

type RunResult struct {
	ExitCode   int
	EvidenceID string
}

// Runner executes one check command with dir as its working directory. A
// non-zero exit is a result, not an error; errors mean the check could not run.
type Runner interface {
	Run(ctx context.Context, dir, command string) (RunResult, error)
}

// ExecRunner runs checks directly on the host. Valid only at trust level none:
// host-local runs inherit the witness's environment and filesystem, and check
// code can follow symlinks out of the checkout and read the witness's files,
// including its signing key.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, dir, command string) (RunResult, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = dir
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return RunResult{ExitCode: exitErr.ExitCode()}, nil
	}
	if err != nil {
		return RunResult{}, err
	}
	return RunResult{}, nil
}

// ContainerRunner runs each check in a throwaway container from the
// constitution's image. It goes through Inner so the host runner (agentsh's
// executor) still records the run as evidence.
//
// Check code is agent-authored, so the container gets the same hardening as a
// maker container (no capabilities, no privilege gain), resource ceilings so
// one check cannot starve the host (or another station's check into a signed
// failure), and a unique name so it can be force-removed whatever ended the
// run. The check runs as the image's user.
type ContainerRunner struct {
	Inner  Runner
	Engine string
	Image  string
	Limits constitution.Limits // zero fields take the constitution defaults
	// Remove force-removes a check container after its run; nil runs
	// `<engine> rm -f -- <name>`.
	Remove func(name string)
	Log    *log.Logger // reports cleanup failures; nil means the standard logger
}

// removeGrace bounds the cleanup of a check container.
const removeGrace = 30 * time.Second

func (c ContainerRunner) remove(name string) {
	if c.Remove != nil {
		c.Remove(name)
		return
	}
	// Fresh context: cleanup must run even when the request was cancelled.
	ctx, cancel := context.WithTimeout(context.Background(), removeGrace)
	defer cancel()
	out, err := exec.CommandContext(ctx, c.Engine, "rm", "-f", "--", name).CombinedOutput()
	if err != nil {
		logger := c.Log
		if logger == nil {
			logger = log.Default()
		}
		logger.Printf("witness: %s rm -f %s: %v: %s", c.Engine, name, err, strings.TrimSpace(string(out)))
	}
}

func (c ContainerRunner) Run(ctx context.Context, dir, command string) (RunResult, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return RunResult{}, err
	}
	name := "sweatshop-check-" + hex.EncodeToString(raw)
	l := constitution.Runner{Memory: c.Limits.Memory, Pids: c.Limits.Pids, CPUs: c.Limits.CPUs}.Limits()
	wrapped := fmt.Sprintf("%s run --rm --name %s --cap-drop=ALL --security-opt=no-new-privileges --memory %s --pids-limit %d --cpus %s -v %s:/src -w /src -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=/src %s bash -c %s",
		c.Engine, shellQuote(name), shellQuote(l.Memory), l.Pids, shellQuote(l.CPUs),
		shellQuote(dir), shellQuote(c.Image), shellQuote(command))
	res, err := c.Inner.Run(ctx, dir, wrapped)
	// Whatever ended the run (exit, timeout, cancel), no check container may
	// outlive it.
	c.remove(name)
	return res, err
}

func NewRunner(spec constitution.Runner, host Runner) Runner {
	if spec.Kind == "container" {
		return ContainerRunner{Inner: host, Engine: spec.Engine, Image: spec.Image, Limits: spec.Limits()}
	}
	return host
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
