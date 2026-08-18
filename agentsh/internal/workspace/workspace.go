package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
)

const stateDirectory = ".agentsh"

type Paths struct {
	Root     string
	StateDir string
	Socket   string
	PID      string
	Log      string
	Database string
	Blobs    string
	Index    string
	Services string
}

func Resolve(start string) (Paths, error) {
	if start == "" {
		var err error
		start, err = os.Getwd()
		if err != nil {
			return Paths{}, err
		}
	}

	root, err := filepath.Abs(start)
	if err != nil {
		return Paths{}, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return Paths{}, err
	}
	if !info.IsDir() {
		root = filepath.Dir(root)
	}

	for dir := root; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			root = dir
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}

	state := filepath.Join(root, stateDirectory)
	return Paths{
		Root: root, StateDir: state, Socket: socketPath(root, state),
		PID: filepath.Join(state, "agentshd.pid"), Log: filepath.Join(state, "agentshd.log"),
		Database: filepath.Join(state, "history.db"), Blobs: filepath.Join(state, "blobs"),
		Index: filepath.Join(state, "index"), Services: filepath.Join(state, "services"),
	}, nil
}

func (p Paths) Ensure() error {
	if p.Root == "" || p.StateDir == "" {
		return errors.New("invalid workspace paths")
	}
	if err := os.MkdirAll(p.Blobs, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(p.Index, 0o700); err != nil {
		return err
	}
	return os.MkdirAll(p.Services, 0o700)
}

// Unix socket paths are short on macOS. Fall back to the temp directory when
// the workspace-local path would exceed the conservative portable limit.
func socketPath(root, state string) string {
	local := filepath.Join(state, "agentshd.sock")
	if len(local) < 100 {
		return local
	}
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(os.TempDir(), "agentshd-"+hex.EncodeToString(sum[:8])+".sock")
}
