package gitx

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// refSource is what a fetch reads from: named object IDs and the objects
// behind them. It is a repository on disk or an unpacked bundle.
type refSource struct {
	refs    []*plumbing.Reference
	objects storer.EncodedObjectStorer
}

// repoSource lists repo's refs with symbolic ones resolved, HEAD included
// unless it is unborn.
func repoSource(repo *git.Repository) (refSource, error) {
	iter, err := repo.References()
	if err != nil {
		return refSource{}, err
	}
	names := map[plumbing.ReferenceName]bool{plumbing.HEAD: true}
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		names[ref.Name()] = true
		return nil
	}); err != nil {
		return refSource{}, err
	}
	src := refSource{objects: repo.Storer}
	for name := range names {
		resolved, err := repo.Reference(name, true)
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			continue // HEAD or a symbolic ref naming a branch that does not exist yet
		}
		if err != nil {
			return refSource{}, err
		}
		src.refs = append(src.refs, plumbing.NewHashReference(name, resolved.Hash()))
	}
	return src, nil
}

// objectCopier copies an object and everything it reaches from src to dst.
// Children are written before their parents, so any object that dst holds has
// its whole closure there too; that lets the copy stop at what dst has.
type objectCopier struct {
	ctx context.Context
	src storer.EncodedObjectStorer
	dst storer.EncodedObjectStorer
}

func (c objectCopier) copy(h plumbing.Hash) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if c.dst.HasEncodedObject(h) == nil {
		return nil
	}
	obj, err := c.src.EncodedObject(plumbing.AnyObject, h)
	if err != nil {
		return fmt.Errorf("copy %s: %w", h, err)
	}
	var children []plumbing.Hash
	switch obj.Type() {
	case plumbing.CommitObject:
		commit, err := object.DecodeCommit(c.src, obj)
		if err != nil {
			return err
		}
		children = append(append(children, commit.ParentHashes...), commit.TreeHash)
	case plumbing.TreeObject:
		tree, err := object.DecodeTree(c.src, obj)
		if err != nil {
			return err
		}
		for _, e := range tree.Entries {
			if e.Mode != filemode.Submodule {
				children = append(children, e.Hash)
			}
		}
	case plumbing.TagObject:
		tag, err := object.DecodeTag(c.src, obj)
		if err != nil {
			return err
		}
		children = append(children, tag.Target)
	}
	for _, child := range children {
		if err := c.copy(child); err != nil {
			return err
		}
	}
	_, err = c.dst.SetEncodedObject(obj)
	return err
}

// Fetch copies the refs of src that the refspecs select, with the objects
// behind them, into r. Refspecs are git's `src:dst` form (a `*` stands for
// any run of characters). Refs are overwritten even without the conventional
// leading `+`. A refspec without `*` must match a ref in src. With prune, refs
// of r that a refspec's dst side covers but that src no longer has are
// deleted. Nothing is read from src beyond refs and objects: no config, hooks,
// or upload-pack.
func (r Repo) Fetch(ctx context.Context, src Repo, prune bool, refspecs ...string) error {
	srcRepo, err := src.open(ctx)
	if err != nil {
		return err
	}
	source, err := repoSource(srcRepo)
	if err != nil {
		return err
	}
	return r.fetchFrom(ctx, source, prune, refspecs)
}

func (r Repo) fetchFrom(ctx context.Context, src refSource, prune bool, refspecs []string) error {
	dst, err := r.open(ctx)
	if err != nil {
		return err
	}
	specs := make([]config.RefSpec, len(refspecs))
	wanted := map[plumbing.ReferenceName]plumbing.Hash{}
	for i, s := range refspecs {
		specs[i] = config.RefSpec(s)
		if err := specs[i].Validate(); err != nil {
			return err
		}
		matched := false
		for _, ref := range src.refs {
			if specs[i].Match(ref.Name()) {
				wanted[specs[i].Dst(ref.Name())] = ref.Hash()
				matched = true
			}
		}
		if !matched && !specs[i].IsWildcard() {
			return fmt.Errorf("source has no ref matching %s", s)
		}
	}
	names := make([]plumbing.ReferenceName, 0, len(wanted))
	for name := range wanted {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	copier := objectCopier{ctx: ctx, src: src.objects, dst: dst.Storer}
	for _, name := range names {
		if err := copier.copy(wanted[name]); err != nil {
			return err
		}
	}
	for _, name := range names {
		if err := setRef(dst, name.String(), wanted[name].String()); err != nil {
			return err
		}
	}
	if !prune {
		return nil
	}
	existing, err := dst.References()
	if err != nil {
		return err
	}
	var stale []plumbing.ReferenceName
	if err := existing.ForEach(func(ref *plumbing.Reference) error {
		if _, kept := wanted[ref.Name()]; kept {
			return nil
		}
		for _, spec := range specs {
			if spec.Reverse().Match(ref.Name()) {
				stale = append(stale, ref.Name())
				break
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for _, name := range stale {
		if err := dst.Storer.RemoveReference(name); err != nil {
			return err
		}
	}
	return nil
}

// PushCommit copies commit and its history from r into dst and creates ref
// there. It does nothing to an existing ref that already holds commit and
// returns ErrRefMoved if the ref holds another value. dst's hooks do not run.
func (r Repo) PushCommit(ctx context.Context, dst Repo, commit, ref string) error {
	srcRepo, err := r.open(ctx)
	if err != nil {
		return err
	}
	dstRepo, err := dst.open(ctx)
	if err != nil {
		return err
	}
	h, err := parseHash(commit)
	if err != nil {
		return err
	}
	if err := (objectCopier{ctx: ctx, src: srcRepo.Storer, dst: dstRepo.Storer}).copy(h); err != nil {
		return err
	}
	err = dst.UpdateRef(ctx, ref, commit, "")
	if errors.Is(err, ErrRefMoved) {
		if current, refErr := dst.Ref(ctx, ref); refErr == nil && current == commit {
			return nil
		}
	}
	return err
}

// CloneBranchBare makes r (which must not exist yet) a bare repository holding
// only branchRef of src, with HEAD pointing at it.
func (r Repo) CloneBranchBare(ctx context.Context, src Repo, branchRef string) error {
	if err := r.Init(ctx, true); err != nil {
		return err
	}
	if err := r.Fetch(ctx, src, false, "+"+branchRef+":"+branchRef); err != nil {
		return err
	}
	repo, err := r.open(ctx)
	if err != nil {
		return err
	}
	return repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName(branchRef)))
}

// CheckoutDetached makes r (an empty directory) a repository holding commit
// and its history, with the commit's files checked out and HEAD detached
// there. The result needs nothing from src afterwards.
func (r Repo) CheckoutDetached(ctx context.Context, src Repo, commit string) error {
	h, err := parseHash(commit)
	if err != nil {
		return err
	}
	srcRepo, err := src.open(ctx)
	if err != nil {
		return err
	}
	if err := r.Init(ctx, false); err != nil {
		return err
	}
	repo, err := r.open(ctx)
	if err != nil {
		return err
	}
	if err := (objectCopier{ctx: ctx, src: srcRepo.Storer, dst: repo.Storer}).copy(h); err != nil {
		return err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return err
	}
	return wt.Checkout(&git.CheckoutOptions{Hash: h, Force: true})
}
