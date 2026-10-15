package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Fewbytes/sweatshop/line/gitx"
)

// Exec runs one argv, without a shell, in dir.
type Exec interface {
	Run(ctx context.Context, dir string, argv ...string) error
}

// OSExec runs argv on this host.
type OSExec struct{}

func (OSExec) Run(ctx context.Context, dir string, argv ...string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		tail := strings.TrimSpace(string(out))
		if len(tail) > 2000 {
			tail = tail[len(tail)-2000:]
		}
		return fmt.Errorf("%s: %w: %s", strings.Join(argv[:min(len(argv), 3)], " "), err, tail)
	}
	return nil
}

// Launchd deploys a binary built from the release commit on the dev Mac:
// deploy/<C>/sweatshop, a `current` symlink swapped to it, and the allowed
// launchd labels kickstarted. Every argv goes through an allowlist, so the
// privileged actuator can run nothing else.
type Launchd struct {
	Dir    string   // deploy directory, holds <C>/ and the current symlink
	Source string   // git repository to build from (the line remote)
	Domain string   // launchd domain target, e.g. gui/501
	Labels []string // allowlist of labels to kickstart
	Probe  HTTPHealth
	Exec   Exec // default OSExec
}

const launchdBinary = "sweatshop"

func (l Launchd) exec() Exec {
	if l.Exec != nil {
		return l.Exec
	}
	return OSExec{}
}

// Allowed reports whether argv is one of the few commands the backend runs.
func (l Launchd) Allowed(argv []string) bool {
	switch {
	case len(argv) == 4 && argv[0] == "launchctl" && argv[1] == "kickstart" && argv[2] == "-k":
		return slices.ContainsFunc(l.Labels, func(label string) bool { return argv[3] == l.Domain+"/"+label })
	case len(argv) == 5 && slices.Equal([]string{argv[0], argv[1], argv[2]}, []string{"go", "build", "-o"}):
		return argv[4] == "./cmd/sweatshop" && strings.HasPrefix(argv[3], filepath.Clean(l.Dir)+string(filepath.Separator))
	}
	return false
}

func (l Launchd) run(ctx context.Context, dir string, argv ...string) error {
	if !l.Allowed(argv) {
		return fmt.Errorf("launchd backend: refusing to run %q", argv)
	}
	return l.exec().Run(ctx, dir, argv...)
}

func (l Launchd) link() string { return filepath.Join(l.Dir, "current") }

func (l Launchd) Current(context.Context) (Release, error) {
	target, err := os.Readlink(l.link())
	if errors.Is(err, os.ErrNotExist) {
		return Release{}, nil
	}
	if err != nil {
		return Release{}, err
	}
	c := filepath.Base(filepath.Dir(target)) // <Dir>/<C>/sweatshop
	if !commitRe.MatchString(c) {
		return Release{}, nil // not deployed by the line
	}
	return Release{Commit: c}, nil
}

func (l Launchd) Apply(ctx context.Context, r Release) error {
	if err := r.Validate(false); err != nil {
		return err
	}
	bin := filepath.Join(l.Dir, r.Commit, launchdBinary)
	if _, err := os.Stat(bin); err != nil {
		if err := l.build(ctx, r.Commit, bin); err != nil {
			return err
		}
	}
	return l.activate(ctx, bin)
}

// build compiles commit from a throwaway checkout into bin.
func (l Launchd) build(ctx context.Context, commit, bin string) error {
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp(l.Dir, "build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	if err := (gitx.Repo{Dir: work}).CheckoutDetached(ctx, gitx.Repo{Dir: l.Source}, commit); err != nil {
		return err
	}
	tmp := bin + ".tmp"
	if err := l.run(ctx, filepath.Join(work, "line"), "go", "build", "-o", tmp, "./cmd/sweatshop"); err != nil {
		return err
	}
	return os.Rename(tmp, bin)
}

// activate points current at bin and restarts the allowed labels.
func (l Launchd) activate(ctx context.Context, bin string) error {
	tmp := l.link() + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(bin, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, l.link()); err != nil {
		return err
	}
	var errs []error
	for _, label := range l.Labels {
		if err := l.run(ctx, "", "launchctl", "kickstart", "-k", l.Domain+"/"+label); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (l Launchd) Rollback(ctx context.Context, prev Release) error {
	if !commitRe.MatchString(prev.Commit) {
		return errors.New("no previous release to roll back to")
	}
	bin := filepath.Join(l.Dir, prev.Commit, launchdBinary)
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("previous release binary: %w", err)
	}
	return l.activate(ctx, bin)
}

func (l Launchd) Health(ctx context.Context) error { return l.Probe.Check(ctx) }
