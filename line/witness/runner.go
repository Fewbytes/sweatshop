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
	// Output is the check's combined output, for per-test results; nil when
	// the runner does not keep it.
	Output []byte
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
//
// Only the check's own outcome is a result. Exit 125 (the engine could not
// start the container) and exit 137 without OOMKilled (the engine, the VM or
// someone else SIGKILLed it) are runner errors: nothing is signed. A 137 from
// the container's own --memory limit is a real check failure. Not guaranteed:
// disk is not limited, so a check or maker can still fill the VM's disk and
// make a check fail for real; and the engine can be starved in ways that
// surface as ordinary non-zero exits of the check's own processes.
type ContainerRunner struct {
	Inner  Runner
	Engine string
	Image  string
	Limits constitution.Limits // zero fields take the constitution defaults
	// OOMKilled reports whether the named container was killed by its own
	// memory limit; nil runs `<engine> inspect --format '{{.State.OOMKilled}}'`.
	// It runs before Remove.
	OOMKilled func(name string) (bool, error)
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

// Exit codes that mean something other than the check's own verdict.
const (
	exitEngineError = 125
	exitSIGKILL     = 137
)

func (c ContainerRunner) oomKilled(name string) (bool, error) {
	if c.OOMKilled != nil {
		return c.OOMKilled(name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), removeGrace)
	defer cancel()
	out, err := exec.CommandContext(ctx, c.Engine, "inspect", "--format", "{{.State.OOMKilled}}", "--", name).Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// classify turns engine failures into errors so they are never signed as
// check failures.
func (c ContainerRunner) classify(name string, code int) error {
	switch code {
	case exitEngineError:
		return fmt.Errorf("container engine could not start the check (exit %d)", code)
	case exitSIGKILL:
		oom, err := c.oomKilled(name)
		if err != nil {
			return fmt.Errorf("check container exited 137 and OOMKilled is unknown: %w", err)
		}
		if !oom {
			return fmt.Errorf("check container was killed (exit 137) but not by its own memory limit")
		}
	}
	return nil
}

func (c ContainerRunner) Run(ctx context.Context, dir, command string) (RunResult, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return RunResult{}, err
	}
	name := "sweatshop-check-" + hex.EncodeToString(raw)
	l := constitution.Runner{Memory: c.Limits.Memory, Pids: c.Limits.Pids, CPUs: c.Limits.CPUs}.Limits()
	// No --rm: the container must outlive the run so classify can inspect
	// OOMKilled; c.remove force-removes it afterwards.
	wrapped := fmt.Sprintf("%s run --name %s --cap-drop=ALL --security-opt=no-new-privileges --memory %s --pids-limit %d --cpus %s -v %s:/src -w /src -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=/src %s bash -c %s",
		c.Engine, shellQuote(name), shellQuote(l.Memory), l.Pids, shellQuote(l.CPUs),
		shellQuote(dir), shellQuote(c.Image), shellQuote(command))
	res, err := c.Inner.Run(ctx, dir, wrapped)
	if err == nil {
		err = c.classify(name, res.ExitCode)
	}
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
