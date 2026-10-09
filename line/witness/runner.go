package witness

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

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

// ExecRunner runs checks directly on the host. Valid only at trust level none.
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
type ContainerRunner struct {
	Inner  Runner
	Engine string
	Image  string
}

func (c ContainerRunner) Run(ctx context.Context, dir, command string) (RunResult, error) {
	wrapped := fmt.Sprintf("%s run --rm -v %s:/src -w /src -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=/src %s bash -c %s",
		c.Engine, shellQuote(dir), shellQuote(c.Image), shellQuote(command))
	return c.Inner.Run(ctx, dir, wrapped)
}

func NewRunner(spec constitution.Runner, host Runner) Runner {
	if spec.Kind == "container" {
		return ContainerRunner{Inner: host, Engine: spec.Engine, Image: spec.Image}
	}
	return host
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
