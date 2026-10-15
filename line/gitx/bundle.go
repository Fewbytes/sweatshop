package gitx

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
)

// Bundles use git's v2 format: a signature line, one `-<id>` line per
// prerequisite commit the receiver must already have, one `<id> <ref>` line
// per ref, a blank line, then a pack.
const bundleSignature = "# v2 git bundle"

// ErrNothingToBundle means the commit is already part of the base's history.
var ErrNothingToBundle = errors.New("commit is already in the base's history")

// packWindow is the delta search window handed to the pack encoder.
const packWindow = 10

// CreateBundle writes a bundle at path carrying commit as ref. When base is
// not empty the bundle omits base's history and lists base as a prerequisite;
// it needs only the history between commit and base, so a shallow r works.
// It returns ErrNothingToBundle when commit is base or one of its ancestors.
func (r Repo) CreateBundle(ctx context.Context, path, ref, commit, base string) error {
	repo, err := r.open(ctx)
	if err != nil {
		return err
	}
	tip, err := resolveCommit(repo, commit)
	if err != nil {
		return err
	}
	var baseCommit *object.Commit
	if base != "" {
		if baseCommit, err = resolveCommit(repo, base); err != nil {
			return err
		}
		if reachableFrom(repo, baseCommit, tip.Hash) {
			return ErrNothingToBundle
		}
	}
	sel := bundleSelection{repo: repo, have: map[plumbing.Hash]bool{}, seen: map[plumbing.Hash]bool{}}
	if baseCommit != nil {
		sel.have[baseCommit.Hash] = true
		if err := sel.addTree(baseCommit.TreeHash, sel.have); err != nil {
			return err
		}
	}
	if err := sel.addCommit(tip); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	fmt.Fprintln(w, bundleSignature)
	if baseCommit != nil {
		fmt.Fprintf(w, "-%s\n", baseCommit.Hash)
	}
	fmt.Fprintf(w, "%s %s\n\n", tip.Hash, ref)
	if _, err = packfile.NewEncoder(w, repo.Storer, false).Encode(sel.objects, packWindow); err == nil {
		err = w.Flush()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// reachableFrom reports whether target is c or one of its ancestors. Commits
// missing from a shallow repository end that line of history.
func reachableFrom(repo *git.Repository, c *object.Commit, target plumbing.Hash) bool {
	seen := map[plumbing.Hash]bool{}
	var visit func(*object.Commit) bool
	visit = func(c *object.Commit) bool {
		if c.Hash == target {
			return true
		}
		if seen[c.Hash] {
			return false
		}
		seen[c.Hash] = true
		for _, p := range c.ParentHashes {
			if parent, err := repo.CommitObject(p); err == nil && visit(parent) {
				return true
			}
		}
		return false
	}
	return visit(c)
}

// bundleSelection collects the objects a bundle must carry: the commits above
// base and whatever they hold that base's tree does not.
type bundleSelection struct {
	repo    *git.Repository
	have    map[plumbing.Hash]bool // base's closure: the receiver has these
	seen    map[plumbing.Hash]bool
	objects []plumbing.Hash
}

func (s *bundleSelection) add(h plumbing.Hash) bool {
	if s.have[h] || s.seen[h] {
		return false
	}
	s.seen[h] = true
	s.objects = append(s.objects, h)
	return true
}

func (s *bundleSelection) addCommit(c *object.Commit) error {
	if !s.add(c.Hash) {
		return nil
	}
	if err := s.addTree(c.TreeHash, nil); err != nil {
		return err
	}
	for _, p := range c.ParentHashes {
		parent, err := s.repo.CommitObject(p)
		if err != nil {
			return fmt.Errorf("history of %s: %w", c.Hash, err)
		}
		if err := s.addCommit(parent); err != nil {
			return err
		}
	}
	return nil
}

// addTree records the tree and its contents; into, when set, is the set to
// record them in instead (used to compute base's closure).
func (s *bundleSelection) addTree(h plumbing.Hash, into map[plumbing.Hash]bool) error {
	if into != nil {
		if into[h] {
			return nil
		}
		into[h] = true
	} else if !s.add(h) {
		return nil
	}
	tree, err := s.repo.TreeObject(h)
	if err != nil {
		return err
	}
	for _, e := range tree.Entries {
		switch {
		case e.Mode == filemode.Submodule:
		case e.Mode == filemode.Dir:
			if err := s.addTree(e.Hash, into); err != nil {
				return err
			}
		case into != nil:
			into[e.Hash] = true
		default:
			s.add(e.Hash)
		}
	}
	return nil
}

// FetchBundle unpacks the bundle at path and stores its refs in r as Fetch
// does for a repository. Every prerequisite must be reachable from a ref of r
// under prerequisitesWithin, and every ref in the bundle must have its whole
// history in the bundle or in r; otherwise nothing is stored.
func (r Repo) FetchBundle(ctx context.Context, path, prerequisitesWithin string, prune bool, refspecs ...string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	prerequisites, refs, err := readBundleHeader(br)
	if err != nil {
		return fmt.Errorf("bundle %s: %w", path, err)
	}
	for _, p := range prerequisites {
		ok, err := r.ReachableFromRefs(ctx, p.String(), prerequisitesWithin)
		if err != nil || !ok {
			return fmt.Errorf("bundle %s: prerequisite %s is not in %s: %v", path, p, prerequisitesWithin, err)
		}
	}
	objects := memory.NewStorage()
	if err := packfile.UpdateObjectStorage(objects, br); err != nil {
		return fmt.Errorf("bundle %s: read pack: %w", path, err)
	}
	return r.fetchFrom(ctx, refSource{refs: refs, objects: objects}, prune, refspecs)
}

func readBundleHeader(br *bufio.Reader) (prerequisites []plumbing.Hash, refs []*plumbing.Reference, err error) {
	line, err := br.ReadString('\n')
	if err != nil || strings.TrimSuffix(line, "\n") != bundleSignature {
		return nil, nil, fmt.Errorf("not a v2 bundle")
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return nil, nil, err
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			return prerequisites, refs, nil
		}
		id, rest, _ := strings.Cut(line, " ")
		if strings.HasPrefix(id, "-") {
			if !plumbing.IsHash(id[1:]) {
				return nil, nil, fmt.Errorf("bad prerequisite %q", line)
			}
			prerequisites = append(prerequisites, plumbing.NewHash(id[1:]))
			continue
		}
		name := plumbing.ReferenceName(rest)
		if !plumbing.IsHash(id) || name.Validate() != nil {
			return nil, nil, fmt.Errorf("bad ref line %q", line)
		}
		refs = append(refs, plumbing.NewHashReference(name, plumbing.NewHash(id)))
	}
}
