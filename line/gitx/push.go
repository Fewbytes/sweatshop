package gitx

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// PushFastForward pushes the refs matching patterns (full ref names or
// patterns with one *, e.g. refs/tags/*) to the repository at url. It never
// forces: a ref that is not a descendant of the remote's value is an error,
// and refs already up to date are not. auth may be nil for a local path.
func (r Repo) PushFastForward(ctx context.Context, url string, auth transport.AuthMethod, patterns []string) error {
	repo, err := r.open(ctx)
	if err != nil {
		return err
	}
	specs := make([]config.RefSpec, len(patterns))
	for i, p := range patterns {
		specs[i] = config.RefSpec(p + ":" + p)
	}
	// Repository.PushContext would look up a configured remote and ignore
	// RemoteURL, so push through a throwaway Remote.
	remote := git.NewRemote(repo.Storer, &config.RemoteConfig{Name: "mirror", URLs: []string{url}})
	err = remote.PushContext(ctx, &git.PushOptions{RemoteName: "mirror", RefSpecs: specs, Auth: auth})
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("push to %s: %w", url, err)
	}
	return nil
}
