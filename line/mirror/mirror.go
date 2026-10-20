// Package mirror publishes a line's refs to the storage mirrors its
// constitution declares (GitHub first). Mirrors are write-only and
// fast-forward only; the line never reads from them. The push credential is a
// per-line deploy key that lives on the host, outside the line directory.
package mirror

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/go-git/go-git/v5/plumbing/transport"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"

	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
)

// Credential file names inside a line's mirror credential directory.
const (
	DeployKeyFile  = "deploy_key"
	KnownHostsFile = "known_hosts"
)

// SSHAuth reads the line's deploy key and pinned host keys from credDir. An
// unreadable key or host list is an error: a mirror push never falls back to
// the invoking user's ssh agent or ~/.ssh.
func SSHAuth(credDir string) (transport.AuthMethod, error) {
	auth, err := gitssh.NewPublicKeysFromFile("git", filepath.Join(credDir, DeployKeyFile), "")
	if err != nil {
		return nil, fmt.Errorf("mirror deploy key: %w", err)
	}
	hostKeys, err := gitssh.NewKnownHostsCallback(filepath.Join(credDir, KnownHostsFile))
	if err != nil {
		return nil, fmt.Errorf("mirror known hosts: %w", err)
	}
	auth.HostKeyCallback = hostKeys
	return auth, nil
}

// Publish pushes each of c's storage mirrors the refs it asks for from repo.
// It tries every mirror and returns the joined failures, naming the mirror
// (never the credential).
func Publish(ctx context.Context, repo gitx.Repo, c constitution.Constitution, auth transport.AuthMethod) error {
	var errs []error
	for _, m := range c.StorageMirrors() {
		if err := repo.PushFastForward(ctx, m.URL, auth, m.PublishRefs()); err != nil {
			errs = append(errs, fmt.Errorf("mirror %q: %w", m.Name, err))
		}
	}
	return errors.Join(errs...)
}
