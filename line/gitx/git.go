// Package gitx runs git for line components. Calls never prompt and never
// read the invoking user's global or system config, so a witness or guard
// behaves the same regardless of whose account it runs under.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var ErrRefMoved = errors.New("ref moved")

type Repo struct {
	Dir string
}

type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.Args, " "), e.Err, e.Stderr)
}

func (e *Error) Unwrap() error { return e.Err }

func (r Repo) Git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", r.Dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", &Error{Args: args, Stderr: strings.TrimSpace(stderr.String()), Err: err}
	}
	return strings.TrimSpace(stdout.String()), nil
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func (r Repo) Commit(ctx context.Context, rev string) (string, error) {
	return r.Git(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
}

func (r Repo) Tree(ctx context.Context, commit string) (string, error) {
	return r.Git(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", commit+"^{tree}")
}

func (r Repo) Parents(ctx context.Context, commit string) ([]string, error) {
	out, err := r.Git(ctx, "rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return nil, fmt.Errorf("commit %s not found", commit)
	}
	return fields[1:], nil
}

func (r Repo) ChangedPaths(ctx context.Context, from, to string) ([]string, error) {
	out, err := r.Git(ctx, "diff", "--name-only", "--no-renames", "-z", from, to)
	if err != nil {
		return nil, err
	}
	out = strings.TrimRight(out, "\x00")
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\x00"), nil
}

func (r Repo) Ref(ctx context.Context, ref string) (string, error) {
	return r.Git(ctx, "rev-parse", "--verify", "--quiet", ref)
}

func (r Repo) RefExists(ctx context.Context, ref string) (bool, error) {
	_, err := r.Git(ctx, "show-ref", "--verify", "--quiet", ref)
	if err == nil {
		return true, nil
	}
	if exitCode(err) == 1 {
		return false, nil
	}
	return false, err
}

// UpdateRef moves ref to newValue only if it currently holds oldValue (an
// empty oldValue means the ref must not exist). git performs the
// compare-and-swap under the ref lock.
func (r Repo) UpdateRef(ctx context.Context, ref, newValue, oldValue string) error {
	_, err := r.Git(ctx, "update-ref", ref, newValue, oldValue)
	if err == nil {
		return nil
	}
	current, readErr := r.Ref(ctx, ref)
	if readErr == nil && current != oldValue {
		return ErrRefMoved
	}
	return err
}

func (r Repo) DeleteRef(ctx context.Context, ref string) error {
	_, err := r.Git(ctx, "update-ref", "-d", ref)
	return err
}
