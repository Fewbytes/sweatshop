// Package gitx reads and writes git repositories for line components, in
// process. Nothing here execs git or reads the invoking user's git config, so
// a witness or guard behaves the same regardless of whose account it runs
// under and the line's hosts need no git installation.
package gitx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

var ErrRefMoved = errors.New("ref moved")

// Repo is a repository on disk, bare or with a work tree, addressed by path.
type Repo struct {
	Dir string
}

// refWrites serializes compare-and-swap ref updates inside this process.
// go-git locks an existing ref file while swapping it, but has no
// create-if-absent or delete-if-equal, so those are check-then-write; the
// line runs one process per remote.
var refWrites sync.Mutex

func (r Repo) open(ctx context.Context) (*git.Repository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, err := os.Stat(filepath.Join(r.Dir, git.GitDirName))
	bare := errors.Is(err, fs.ErrNotExist)
	if err != nil && !bare {
		return nil, err
	}
	dot, wt := repoFilesystems(r.Dir, bare)
	return git.Open(newStorage(dot), wt)
}

// Init creates an empty repository at r.Dir; HEAD points at refs/heads/master.
func (r Repo) Init(ctx context.Context, bare bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dot, wt := repoFilesystems(r.Dir, bare)
	_, err := git.Init(newStorage(dot), wt)
	return err
}

// resolve turns rev (an object ID, a ref name, HEAD, ...) into an object ID
// that exists in the repository. A ref is not peeled: it resolves to the
// object it holds, whatever its type.
func resolve(repo *git.Repository, rev string) (plumbing.Hash, error) {
	h := plumbing.NewHash(rev)
	if !plumbing.IsHash(rev) {
		if ref, err := repo.Reference(plumbing.ReferenceName(rev), true); err == nil {
			h = ref.Hash()
		} else {
			resolved, err := repo.ResolveRevision(plumbing.Revision(rev))
			if err != nil {
				return plumbing.ZeroHash, fmt.Errorf("resolve %q: %w", rev, err)
			}
			h = *resolved
		}
	}
	if err := repo.Storer.HasEncodedObject(h); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("resolve %q: %w", rev, err)
	}
	return h, nil
}

// resolveCommit is resolve, peeling annotated tags down to a commit.
func resolveCommit(repo *git.Repository, rev string) (*object.Commit, error) {
	h, err := resolve(repo, rev)
	if err != nil {
		return nil, err
	}
	if tag, err := repo.TagObject(h); err == nil {
		return tag.Commit()
	}
	return repo.CommitObject(h)
}

func parseHash(id string) (plumbing.Hash, error) {
	if !plumbing.IsHash(id) {
		return plumbing.ZeroHash, fmt.Errorf("%q is not a full object id", id)
	}
	return plumbing.NewHash(id), nil
}

// Commit returns the object ID of the commit rev names.
func (r Repo) Commit(ctx context.Context, rev string) (string, error) {
	repo, err := r.open(ctx)
	if err != nil {
		return "", err
	}
	c, err := resolveCommit(repo, rev)
	if err != nil {
		return "", err
	}
	return c.Hash.String(), nil
}

// Tree returns the object ID of the tree of the commit rev names.
func (r Repo) Tree(ctx context.Context, rev string) (string, error) {
	repo, err := r.open(ctx)
	if err != nil {
		return "", err
	}
	c, err := resolveCommit(repo, rev)
	if err != nil {
		return "", err
	}
	return c.TreeHash.String(), nil
}

func (r Repo) Parents(ctx context.Context, commit string) ([]string, error) {
	repo, err := r.open(ctx)
	if err != nil {
		return nil, err
	}
	c, err := resolveCommit(repo, commit)
	if err != nil {
		return nil, err
	}
	var parents []string
	for _, p := range c.ParentHashes {
		parents = append(parents, p.String())
	}
	return parents, nil
}

// ChangedPaths lists the paths whose content or mode differs between the
// trees of from and to, sorted, without rename detection.
func (r Repo) ChangedPaths(ctx context.Context, from, to string) ([]string, error) {
	repo, err := r.open(ctx)
	if err != nil {
		return nil, err
	}
	trees := make([]*object.Tree, 0, 2)
	for _, rev := range []string{from, to} {
		c, err := resolveCommit(repo, rev)
		if err != nil {
			return nil, err
		}
		t, err := c.Tree()
		if err != nil {
			return nil, err
		}
		trees = append(trees, t)
	}
	changes, err := object.DiffTreeWithOptions(ctx, trees[0], trees[1], &object.DiffTreeOptions{DetectRenames: false})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var paths []string
	for _, c := range changes {
		for _, name := range []string{c.From.Name, c.To.Name} {
			if name != "" && !seen[name] {
				seen[name] = true
				paths = append(paths, name)
			}
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// Ref returns the object ID rev names, which must exist.
func (r Repo) Ref(ctx context.Context, ref string) (string, error) {
	repo, err := r.open(ctx)
	if err != nil {
		return "", err
	}
	h, err := resolve(repo, ref)
	if err != nil {
		return "", err
	}
	return h.String(), nil
}

func (r Repo) RefExists(ctx context.Context, ref string) (bool, error) {
	repo, err := r.open(ctx)
	if err != nil {
		return false, err
	}
	_, err = repo.Reference(plumbing.ReferenceName(ref), false)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return false, nil
	}
	return err == nil, err
}

// SetRef points ref at the existing object newValue, whatever it held.
func (r Repo) SetRef(ctx context.Context, ref, newValue string) error {
	repo, err := r.open(ctx)
	if err != nil {
		return err
	}
	return setRef(repo, ref, newValue)
}

func setRef(repo *git.Repository, ref, newValue string) error {
	name := plumbing.ReferenceName(ref)
	if err := name.Validate(); err != nil {
		return err
	}
	h, err := parseHash(newValue)
	if err != nil {
		return err
	}
	if err := repo.Storer.HasEncodedObject(h); err != nil {
		return fmt.Errorf("set %s: %w", ref, err)
	}
	return repo.Storer.SetReference(plumbing.NewHashReference(name, h))
}

// UpdateRef moves ref to newValue only if it currently holds oldValue (an
// empty oldValue means the ref must not exist); otherwise it returns
// ErrRefMoved.
func (r Repo) UpdateRef(ctx context.Context, ref, newValue, oldValue string) error {
	repo, err := r.open(ctx)
	if err != nil {
		return err
	}
	refWrites.Lock()
	defer refWrites.Unlock()
	current, err := repo.Reference(plumbing.ReferenceName(ref), true)
	switch {
	case errors.Is(err, plumbing.ErrReferenceNotFound):
		if oldValue != "" {
			return ErrRefMoved
		}
		return setRef(repo, ref, newValue)
	case err != nil:
		return err
	case oldValue == "" || current.Hash().String() != oldValue:
		return ErrRefMoved
	}
	return setRef(repo, ref, newValue)
}

// WriteBlob stores data as a loose blob and returns its object ID.
func (r Repo) WriteBlob(ctx context.Context, data []byte) (string, error) {
	repo, err := r.open(ctx)
	if err != nil {
		return "", err
	}
	obj := repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	if err := writeAll(obj, data); err != nil {
		return "", err
	}
	h, err := repo.Storer.SetEncodedObject(obj)
	return h.String(), err
}

func writeAll(obj plumbing.EncodedObject, data []byte) error {
	w, err := obj.Writer()
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	return w.Close()
}

// ReadBlob returns the content of a blob named by object ID, or of a file
// named `<rev>:<path>`.
func (r Repo) ReadBlob(ctx context.Context, id string) ([]byte, error) {
	repo, err := r.open(ctx)
	if err != nil {
		return nil, err
	}
	var blob *object.Blob
	if rev, path, ok := strings.Cut(id, ":"); ok {
		c, err := resolveCommit(repo, rev)
		if err != nil {
			return nil, err
		}
		f, err := c.File(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", id, err)
		}
		blob = &f.Blob
	} else {
		h, err := parseHash(id)
		if err != nil {
			return nil, err
		}
		if blob, err = repo.BlobObject(h); err != nil {
			return nil, fmt.Errorf("read blob %s: %w", id, err)
		}
	}
	rd, err := blob.Reader()
	if err != nil {
		return nil, err
	}
	defer rd.Close()
	return io.ReadAll(rd)
}

// ObjectType returns "commit", "tree", "blob" or "tag" for the object id.
func (r Repo) ObjectType(ctx context.Context, id string) (string, error) {
	repo, err := r.open(ctx)
	if err != nil {
		return "", err
	}
	h, err := parseHash(id)
	if err != nil {
		return "", err
	}
	obj, err := repo.Storer.EncodedObject(plumbing.AnyObject, h)
	if err != nil {
		return "", fmt.Errorf("object %s: %w", id, err)
	}
	return obj.Type().String(), nil
}

// DeleteRefIf deletes ref only if it currently holds oldValue; ErrRefMoved
// means it holds another value or is already gone.
func (r Repo) DeleteRefIf(ctx context.Context, ref, oldValue string) error {
	repo, err := r.open(ctx)
	if err != nil {
		return err
	}
	refWrites.Lock()
	defer refWrites.Unlock()
	current, err := repo.Reference(plumbing.ReferenceName(ref), true)
	switch {
	case errors.Is(err, plumbing.ErrReferenceNotFound):
		return ErrRefMoved
	case err != nil:
		return err
	case current.Hash().String() != oldValue:
		return ErrRefMoved
	}
	return repo.Storer.RemoveReference(plumbing.ReferenceName(ref))
}

// DeleteRef deletes ref; deleting a missing ref is not an error.
func (r Repo) DeleteRef(ctx context.Context, ref string) error {
	repo, err := r.open(ctx)
	if err != nil {
		return err
	}
	return repo.Storer.RemoveReference(plumbing.ReferenceName(ref))
}

// RefNames lists the refs whose names start with prefix (which should end in
// "/"), sorted.
func (r Repo) RefNames(ctx context.Context, prefix string) ([]string, error) {
	repo, err := r.open(ctx)
	if err != nil {
		return nil, err
	}
	return refNames(repo, prefix)
}

func refNames(repo *git.Repository, prefix string) ([]string, error) {
	iter, err := repo.References()
	if err != nil {
		return nil, err
	}
	var names []string
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		if strings.HasPrefix(ref.Name().String(), prefix) {
			names = append(names, ref.Name().String())
		}
		return nil
	})
	sort.Strings(names)
	return names, err
}

// ReachableFromRefs reports whether commit is the tip of, or an ancestor of
// the tip of, a ref whose name starts with prefix.
func (r Repo) ReachableFromRefs(ctx context.Context, commit, prefix string) (bool, error) {
	repo, err := r.open(ctx)
	if err != nil {
		return false, err
	}
	target, err := repo.CommitObject(plumbing.NewHash(commit))
	if err != nil {
		return false, err
	}
	names, err := refNames(repo, prefix)
	if err != nil {
		return false, err
	}
	for _, name := range names {
		ref, err := repo.Reference(plumbing.ReferenceName(name), true)
		if err != nil {
			return false, err
		}
		tip, err := resolveCommit(repo, ref.Hash().String())
		if err != nil {
			continue // a ref to a tree or blob holds no commits
		}
		if tip.Hash == target.Hash {
			return true, nil
		}
		if ok, err := target.IsAncestor(tip); err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

// IsAncestor reports whether commit is an ancestor of, or equal to, descendant.
func (r Repo) IsAncestor(ctx context.Context, commit, descendant string) (bool, error) {
	repo, err := r.open(ctx)
	if err != nil {
		return false, err
	}
	a, err := resolveCommit(repo, commit)
	if err != nil {
		return false, err
	}
	d, err := resolveCommit(repo, descendant)
	if err != nil {
		return false, err
	}
	if a.Hash == d.Hash {
		return true, nil
	}
	return a.IsAncestor(d)
}

// LogEntry is one commit as listed by History.
type LogEntry struct {
	Commit  string
	Subject string
}

// History lists the commits reachable from rev, newest first.
func (r Repo) History(ctx context.Context, rev string) ([]LogEntry, error) {
	repo, err := r.open(ctx)
	if err != nil {
		return nil, err
	}
	tip, err := resolveCommit(repo, rev)
	if err != nil {
		return nil, err
	}
	iter, err := repo.Log(&git.LogOptions{From: tip.Hash, Order: git.LogOrderCommitterTime})
	if err != nil {
		return nil, err
	}
	var entries []LogEntry
	err = iter.ForEach(func(c *object.Commit) error {
		subject, _, _ := strings.Cut(c.Message, "\n")
		entries = append(entries, LogEntry{c.Hash.String(), subject})
		return nil
	})
	return entries, err
}

// CommitsSince lists the commits reachable from tip but not from base.
func (r Repo) CommitsSince(ctx context.Context, base, tip string) ([]string, error) {
	repo, err := r.open(ctx)
	if err != nil {
		return nil, err
	}
	known := map[plumbing.Hash]bool{}
	baseCommit, err := resolveCommit(repo, base)
	if err != nil {
		return nil, err
	}
	if err := object.NewCommitPreorderIter(baseCommit, nil, nil).ForEach(func(c *object.Commit) error {
		known[c.Hash] = true
		return nil
	}); err != nil {
		return nil, err
	}
	tipCommit, err := resolveCommit(repo, tip)
	if err != nil {
		return nil, err
	}
	var commits []string
	err = object.NewCommitPreorderIter(tipCommit, known, nil).ForEach(func(c *object.Commit) error {
		commits = append(commits, c.Hash.String())
		return nil
	})
	return commits, err
}

// Message returns the full message of the commit rev names.
func (r Repo) Message(ctx context.Context, rev string) (string, error) {
	repo, err := r.open(ctx)
	if err != nil {
		return "", err
	}
	c, err := resolveCommit(repo, rev)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(c.Message), nil
}

var trailerKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)

// trailerLine matches a `Key: value` trailer line.
var trailerLine = regexp.MustCompile(`^([A-Za-z0-9-]+)[ \t]*:[ \t]*(.*)$`)

// Trailer returns the values of the commit message trailer key, in order. The
// trailers are the message's last paragraph, provided it is not the subject
// paragraph and every line in it is a `Key: value` line or a continuation of
// one.
func (r Repo) Trailer(ctx context.Context, commit, key string) ([]string, error) {
	if !trailerKey.MatchString(key) {
		return nil, fmt.Errorf("invalid trailer key %q", key)
	}
	message, err := r.Message(ctx, commit)
	if err != nil {
		return nil, err
	}
	paragraphs := strings.Split(message, "\n\n")
	if len(paragraphs) < 2 {
		return nil, nil
	}
	var values []string
	matching := false
	for _, line := range strings.Split(paragraphs[len(paragraphs)-1], "\n") {
		if line != "" && (line[0] == ' ' || line[0] == '\t') {
			if matching {
				values[len(values)-1] += "\n" + strings.TrimSpace(line)
			}
			continue
		}
		m := trailerLine.FindStringSubmatch(line)
		if m == nil {
			return nil, nil
		}
		matching = strings.EqualFold(m[1], key)
		if matching {
			values = append(values, m[2])
		}
	}
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
	}
	return values, nil
}

// WriteTreeWithBlob stores a tree holding the single file name -> blob and
// returns the tree's object ID.
func (r Repo) WriteTreeWithBlob(ctx context.Context, name, blob string) (string, error) {
	if !trailerKey.MatchString(name) {
		return "", fmt.Errorf("invalid file name %q", name)
	}
	repo, err := r.open(ctx)
	if err != nil {
		return "", err
	}
	blobHash, err := parseHash(blob)
	if err != nil {
		return "", err
	}
	tree := object.Tree{Entries: []object.TreeEntry{{Name: name, Mode: filemode.Regular, Hash: blobHash}}}
	return storeEncoded(repo, &tree)
}

// CommitTree creates a commit of tree with the given parent ("" for none) and
// message, authored as who, and returns its object ID. It does not move any ref.
func (r Repo) CommitTree(ctx context.Context, tree, parent, message, who string) (string, error) {
	repo, err := r.open(ctx)
	if err != nil {
		return "", err
	}
	treeHash, err := parseHash(tree)
	if err != nil {
		return "", err
	}
	sig := object.Signature{Name: who, Email: who + "@line.local", When: time.Now()}
	commit := object.Commit{Author: sig, Committer: sig, Message: strings.TrimRight(message, "\n") + "\n", TreeHash: treeHash}
	if parent != "" {
		parentHash, err := parseHash(parent)
		if err != nil {
			return "", err
		}
		commit.ParentHashes = []plumbing.Hash{parentHash}
	}
	return storeEncoded(repo, &commit)
}

type encodable interface {
	Encode(plumbing.EncodedObject) error
}

func storeEncoded(repo *git.Repository, o encodable) (string, error) {
	obj := repo.Storer.NewEncodedObject()
	if err := o.Encode(obj); err != nil {
		return "", err
	}
	h, err := repo.Storer.SetEncodedObject(obj)
	return h.String(), err
}

// PruneUnreachable deletes loose objects that no ref reaches and that are
// older than grace. Objects inside packs are kept.
func (r Repo) PruneUnreachable(ctx context.Context, grace time.Duration) error {
	repo, err := r.open(ctx)
	if err != nil {
		return err
	}
	return repo.Prune(git.PruneOptions{OnlyObjectsOlderThan: time.Now().Add(-grace), Handler: repo.DeleteObject})
}
