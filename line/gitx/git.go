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
	"regexp"
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
	return r.gitRaw(ctx, true, args...)
}

func (r Repo) gitRaw(ctx context.Context, trimOutput bool, args ...string) (string, error) {
	return runGit(r.command(ctx, args), trimOutput, args)
}

func (r Repo) gitStdin(ctx context.Context, stdin []byte, args ...string) (string, error) {
	cmd := r.command(ctx, args)
	cmd.Stdin = bytes.NewReader(stdin)
	return runGit(cmd, true, args)
}

// command builds a git invocation with the isolated environment.
func (r Repo) command(ctx context.Context, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", r.Dir}, args...)...)

	// Filter out GIT_* variables from parent environment to prevent redirection to another repo
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			env = append(env, e)
		}
	}
	cmd.Env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	return cmd
}

func runGit(cmd *exec.Cmd, trimOutput bool, args []string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", &Error{Args: args, Stderr: strings.TrimSpace(stderr.String()), Err: err}
	}
	out := stdout.String()
	if trimOutput {
		out = strings.TrimSpace(out)
	}
	return out, nil
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func rejectOptionInjection(arg string) error {
	if strings.HasPrefix(arg, "-") {
		return fmt.Errorf("argument must not start with '-': %q", arg)
	}
	return nil
}

func (r Repo) Commit(ctx context.Context, rev string) (string, error) {
	return r.Git(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
}

func (r Repo) Tree(ctx context.Context, commit string) (string, error) {
	return r.Git(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", commit+"^{tree}")
}

func (r Repo) Parents(ctx context.Context, commit string) ([]string, error) {
	if err := rejectOptionInjection(commit); err != nil {
		return nil, err
	}
	out, err := r.Git(ctx, "rev-list", "--parents", "-n", "1", "--end-of-options", commit)
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
	if err := rejectOptionInjection(from); err != nil {
		return nil, err
	}
	if err := rejectOptionInjection(to); err != nil {
		return nil, err
	}
	out, err := r.gitRaw(ctx, false, "diff", "--name-only", "--no-renames", "-z", "--end-of-options", from, to)
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
	if err := rejectOptionInjection(ref); err != nil {
		return "", err
	}
	return r.Git(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", ref)
}

func (r Repo) RefExists(ctx context.Context, ref string) (bool, error) {
	if err := rejectOptionInjection(ref); err != nil {
		return false, err
	}
	_, err := r.Git(ctx, "show-ref", "--verify", "--quiet", "--end-of-options", ref)
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
	if err := rejectOptionInjection(ref); err != nil {
		return err
	}
	if err := rejectOptionInjection(newValue); err != nil {
		return err
	}
	if err := rejectOptionInjection(oldValue); err != nil {
		return err
	}
	_, err := r.Git(ctx, "update-ref", "--end-of-options", ref, newValue, oldValue)
	if err == nil {
		return nil
	}
	// update-ref failed; check if the ref was moved or deleted
	current, readErr := r.gitRaw(ctx, true, "rev-parse", "--verify", "--quiet", "--end-of-options", ref)
	if readErr != nil {
		// ref doesn't exist; if we expected a value, the CAS failed
		if exitCode(readErr) == 1 && oldValue != "" {
			return ErrRefMoved
		}
		return err
	}
	// ref exists; if it doesn't match oldValue, the CAS failed
	if current != oldValue {
		return ErrRefMoved
	}
	return err
}

// WriteBlob stores data as a loose blob and returns its object ID.
func (r Repo) WriteBlob(ctx context.Context, data []byte) (string, error) {
	return r.gitStdin(ctx, data, "hash-object", "-w", "--stdin")
}

func (r Repo) ReadBlob(ctx context.Context, id string) ([]byte, error) {
	if err := rejectOptionInjection(id); err != nil {
		return nil, err
	}
	out, err := r.gitRaw(ctx, false, "cat-file", "blob", id)
	return []byte(out), err
}

// DeleteRefIf deletes ref only if it currently holds oldValue; ErrRefMoved
// means it holds another value or is already gone.
func (r Repo) DeleteRefIf(ctx context.Context, ref, oldValue string) error {
	if err := rejectOptionInjection(ref); err != nil {
		return err
	}
	if err := rejectOptionInjection(oldValue); err != nil {
		return err
	}
	_, err := r.Git(ctx, "update-ref", "-d", "--end-of-options", ref, oldValue)
	if err == nil {
		return nil
	}
	current, readErr := r.Ref(ctx, ref)
	if readErr != nil {
		if exitCode(readErr) == 1 {
			return ErrRefMoved
		}
		return err
	}
	if current != oldValue {
		return ErrRefMoved
	}
	return err
}

var trailerKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)

// Trailer returns the values of the commit message trailer key, in order.
func (r Repo) Trailer(ctx context.Context, commit, key string) ([]string, error) {
	if err := rejectOptionInjection(commit); err != nil {
		return nil, err
	}
	if !trailerKey.MatchString(key) {
		return nil, fmt.Errorf("invalid trailer key %q", key)
	}
	out, err := r.gitRaw(ctx, false, "log", "-1", "--format=%(trailers:key="+key+",valueonly,separator=%x00)", "--end-of-options", commit)
	if err != nil {
		return nil, err
	}
	var values []string
	for _, v := range strings.Split(out, "\x00") {
		if v = strings.TrimSpace(v); v != "" {
			values = append(values, v)
		}
	}
	return values, nil
}

func (r Repo) DeleteRef(ctx context.Context, ref string) error {
	if err := rejectOptionInjection(ref); err != nil {
		return err
	}
	_, err := r.Git(ctx, "update-ref", "-d", "--end-of-options", ref)
	return err
}

// WriteTreeWithBlob stores a tree holding the single file name -> blob and
// returns the tree's object ID.
func (r Repo) WriteTreeWithBlob(ctx context.Context, name, blob string) (string, error) {
	if !trailerKey.MatchString(name) {
		return "", fmt.Errorf("invalid file name %q", name)
	}
	return r.gitStdin(ctx, []byte("100644 blob "+blob+"\t"+name+"\n"), "mktree")
}

// CommitTree creates a commit of tree with the given parent ("" for none) and
// message, authored as who, and returns its object ID. It does not move any ref.
func (r Repo) CommitTree(ctx context.Context, tree, parent, message, who string) (string, error) {
	if err := rejectOptionInjection(tree); err != nil {
		return "", err
	}
	if err := rejectOptionInjection(parent); err != nil {
		return "", err
	}
	args := []string{"commit-tree", tree, "-m", message}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	cmd := r.command(ctx, args)
	email := who + "@line.local"
	cmd.Env = append(cmd.Env,
		"GIT_AUTHOR_NAME="+who, "GIT_AUTHOR_EMAIL="+email,
		"GIT_COMMITTER_NAME="+who, "GIT_COMMITTER_EMAIL="+email)
	return runGit(cmd, true, args)
}
