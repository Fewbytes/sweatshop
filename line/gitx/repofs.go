package gitx

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// Modes git itself gives repository files. go-git creates temp files 0600 and
// directories through the umask, which locks out other uids (station
// containers run as uid 1000 and clone remote.git by path).
const (
	objectFileMode os.FileMode = 0o444
	refFileMode    os.FileMode = 0o644
	repoDirMode    os.FileMode = 0o755
)

// sharedModeFS is a repository filesystem that gives every file and directory
// it creates the modes git would, whatever the umask.
type sharedModeFS struct {
	billy.Filesystem
	root string // OS path of the filesystem root; chroot'd billy filesystems lack Chmod
}

func (fs sharedModeFS) fileMode(name string) os.FileMode {
	if strings.HasPrefix(filepath.ToSlash(name), "objects/") {
		return objectFileMode
	}
	return refFileMode
}

// Create chmods after opening, which leaves the handle writable even for 0444.
func (fs sharedModeFS) Create(name string) (billy.File, error) {
	f, err := fs.Filesystem.Create(name)
	if err != nil {
		return nil, err
	}
	return f, fs.chmod(name, fs.fileMode(name))
}

func (fs sharedModeFS) OpenFile(name string, flag int, perm os.FileMode) (billy.File, error) {
	f, err := fs.Filesystem.OpenFile(name, flag, perm)
	if err != nil || flag&os.O_CREATE == 0 {
		return f, err
	}
	return f, fs.chmod(name, fs.fileMode(name))
}

func (fs sharedModeFS) Rename(oldpath, newpath string) error {
	if err := fs.Filesystem.Rename(oldpath, newpath); err != nil {
		return err
	}
	// osfs creates missing parent directories of newpath itself.
	if err := fs.chmodDirs(filepath.Dir(newpath)); err != nil {
		return err
	}
	return fs.chmod(newpath, fs.fileMode(newpath))
}

func (fs sharedModeFS) MkdirAll(path string, perm os.FileMode) error {
	if err := fs.Filesystem.MkdirAll(path, perm); err != nil {
		return err
	}
	return fs.chmodDirs(path)
}

// chmodDirs fixes dir and its ancestors up to the root, since the umask
// applied to each one that was created.
func (fs sharedModeFS) chmodDirs(dir string) error {
	for dir = filepath.Clean(dir); ; dir = filepath.Dir(dir) {
		if err := fs.chmod(dir, repoDirMode); err != nil {
			return err
		}
		if dir == "." || dir == string(filepath.Separator) {
			return nil
		}
	}
}

func (fs sharedModeFS) chmod(name string, mode os.FileMode) error {
	return os.Chmod(filepath.Join(fs.root, name), mode)
}

// repoFilesystems returns the git directory and, for a non-bare repository,
// the work tree at dir.
func repoFilesystems(dir string, bare bool) (dot, wt billy.Filesystem) {
	if bare {
		return sharedModeFS{osfs.New(dir), dir}, nil
	}
	wt = osfs.New(dir)
	dotGit, _ := wt.Chroot(git.GitDirName)
	return sharedModeFS{dotGit, filepath.Join(dir, git.GitDirName)}, wt
}

func newStorage(dot billy.Filesystem) *filesystem.Storage {
	return filesystem.NewStorage(dot, cache.NewObjectLRUDefault())
}
