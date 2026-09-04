package detector

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

// Runner executes a detector process. It exists so raw output can later be
// routed through agentsh for retention without changing any adapter.
type Runner interface {
	// Run executes name with args in dir. A non-zero exit is not an error:
	// analysis tools signal findings that way. err is reserved for failures to
	// execute at all.
	Run(ctx context.Context, name string, args []string, dir string) (stdout, stderr string, exitCode int, rawRef string, err error)
}

// NewExecRunner returns a Runner backed by os/exec, retaining nothing.
func NewExecRunner() Runner { return execRunner{} }

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args []string, dir string) (string, string, int, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return stdout.String(), stderr.String(), 0, "", nil
	case errors.As(err, &exitErr):
		return stdout.String(), stderr.String(), exitErr.ExitCode(), "", nil
	default:
		return stdout.String(), stderr.String(), -1, "", err
	}
}

// lookPath reports whether a binary is on PATH.
func lookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
