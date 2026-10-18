package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DoltRef is the git ref depmaint's Dolt database syncs under. Beads owns
// refs/dolt/data; two tools must never write one ref (spec: Storage).
const DoltRef = "refs/dolt/depmaint/data"

const (
	doltRemoteName = "origin"
	doltBranch     = "main"
)

// CommandRunner runs name with args in dir and returns its stdout.
type CommandRunner func(ctx context.Context, dir, name string, args ...string) (string, error)

// ExecRunner is the CommandRunner that runs real processes.
func ExecRunner(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, stderr)
	}
	return string(out), nil
}

// Sync pulls then pushes the campaign Dolt database at .depmaint/dolt over
// the repo's git remote, under DoltRef. It initializes the database and its
// remote on first use. Any failing step aborts and is returned: a sync that
// half-succeeds silently would lose verdicts.
func Sync(ctx context.Context, root string, run CommandRunner, w io.Writer) error {
	doltDir := Paths(root).Dolt
	url, err := run(ctx, root, "git", "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("sync needs a git remote named origin: %w", err)
	}
	url = strings.TrimSpace(url)

	if _, err := os.Stat(filepath.Join(doltDir, ".dolt")); os.IsNotExist(err) {
		if err := os.MkdirAll(doltDir, 0o755); err != nil {
			return err
		}
		if _, err := run(ctx, doltDir, "dolt", "init"); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	remotes, err := run(ctx, doltDir, "dolt", "remote", "-v")
	if err != nil {
		return err
	}
	if !strings.Contains(remotes, doltRemoteName) {
		if _, err := run(ctx, doltDir, "dolt", "remote", "add", "--ref", DoltRef, doltRemoteName, url); err != nil {
			return err
		}
	}
	if _, err := run(ctx, doltDir, "dolt", "pull", doltRemoteName, doltBranch); err != nil {
		return err
	}
	if _, err := run(ctx, doltDir, "dolt", "push", doltRemoteName, doltBranch); err != nil {
		return err
	}
	fmt.Fprintf(w, "synced %s with %s via %s\n", doltDir, url, DoltRef)
	return nil
}
