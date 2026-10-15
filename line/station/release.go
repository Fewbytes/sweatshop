package station

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Fewbytes/sweatshop/line/deploy"
	"github.com/Fewbytes/sweatshop/line/gitx"
)

// LineDockerfile is the host component image's Dockerfile in the commit.
const LineDockerfile = "station/Dockerfile.line"

// EngineReleaseBuilder builds sweatshop-line:<C> from a clean checkout of the
// commit (the way viceroy images are built), pushes it, and records the
// digest the registry assigned. Run is the engine CLI: Run(ctx, args...)
// returns its stdout.
type EngineReleaseBuilder struct {
	Repo       gitx.Repo // the line remote
	Image      string    // repository, e.g. registry.example/sweatshop-line
	Dockerfile string    // default LineDockerfile
	Run        func(ctx context.Context, args ...string) (string, error)
}

func (b EngineReleaseBuilder) Build(ctx context.Context, commit string) (deploy.Release, error) {
	dir, cleanup, err := checkoutCommit(ctx, b.Repo, commit)
	if err != nil {
		return deploy.Release{}, fmt.Errorf("checkout %s: %w", commit, err)
	}
	defer cleanup()
	df := b.Dockerfile
	if df == "" {
		df = LineDockerfile
	}
	tag := b.Image + ":" + commit
	if _, err := b.Run(ctx, "build", "-f", filepath.Join(dir, df), "-t", tag, dir); err != nil {
		return deploy.Release{}, err
	}
	if _, err := b.Run(ctx, "push", tag); err != nil {
		return deploy.Release{}, err
	}
	out, err := b.Run(ctx, "image", "inspect", "--format", "{{json .RepoDigests}}", tag)
	if err != nil {
		return deploy.Release{}, err
	}
	var digests []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &digests); err != nil {
		return deploy.Release{}, fmt.Errorf("parse repo digests: %w", err)
	}
	for _, d := range digests {
		if repo, digest, ok := strings.Cut(d, "@"); ok && repo == b.Image {
			return deploy.Release{Commit: commit, Image: b.Image, Digest: digest}, nil
		}
	}
	return deploy.Release{}, fmt.Errorf("%s has no registry digest for %s after push", tag, b.Image)
}

// checkoutCommit materialises commit in a fresh directory.
func checkoutCommit(ctx context.Context, repo gitx.Repo, commit string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "viceroy-checkout-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := (gitx.Repo{Dir: dir}).CheckoutDetached(ctx, repo, commit); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}
