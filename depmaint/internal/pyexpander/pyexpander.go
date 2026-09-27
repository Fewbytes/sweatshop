// Package pyexpander implements the depmaint Python expander: it reads
// resolved dependency truth directly from uv's own lockfile (uv.lock)
// rather than shelling out to a tool and screen-scraping text output. Never
// invokes `uv lock`/`uv sync` — those would hit the network or mutate the
// lockfile, and depmaint's expanders are read-only observers.
package pyexpander

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
	"github.com/pelletier/go-toml/v2"
)

func init() {
	expander.Register(Expander{})
}

// Expander implements expander.Expander for uv-managed Python projects.
type Expander struct{}

func (Expander) Name() string { return "python" }

// Available reports whether the uv toolchain is installed. It says nothing
// about whether any particular root is a uv-managed Python project — see
// Expand. (Unlike the Go expander, Available doesn't gate whether Expand can
// run at all, since Expand only reads a static file — but it is still
// reported the same way, for consistency with the Expander contract and so
// callers can distinguish "uv not installed" from "no uv.lock here" if they
// ever need to.)
func (Expander) Available() bool {
	_, err := exec.LookPath("uv")
	return err == nil
}

// Expand reads root's dependency graph from uv.lock. A root with no
// uv.lock is not an error — it simply contributes no nodes or edges (nil,
// nil, nil), since not every repo root is a uv-managed Python project.
func (Expander) Expand(root string) ([]expander.Node, []expander.Edge, error) {
	if _, err := os.Stat(filepath.Join(root, "uv.lock")); err != nil {
		return nil, nil, nil
	}
	return expand(root)
}

// lockFile mirrors the fields we use from uv.lock. See this plan's Global
// Constraints for the schema this was verified against.
type lockFile struct {
	Package []lockPackage `toml:"package"`
}

type lockPackage struct {
	Name         string            `toml:"name"`
	Version      string            `toml:"version"`
	Source       map[string]string `toml:"source"`
	Dependencies []lockDependency  `toml:"dependencies"`
}

type lockDependency struct {
	Name    string `toml:"name"`
	Version string `toml:"version"` // only present when the target itself resolved to multiple versions
}

// parseLock reads and decodes root's uv.lock.
func parseLock(root string) (lockFile, error) {
	data, err := os.ReadFile(filepath.Join(root, "uv.lock"))
	if err != nil {
		return lockFile{}, fmt.Errorf("reading uv.lock: %w", err)
	}
	var lf lockFile
	if err := toml.Unmarshal(data, &lf); err != nil {
		return lockFile{}, fmt.Errorf("parsing uv.lock: %w", err)
	}
	return lf, nil
}

// isMainPackage reports whether p is the root project itself: its source is
// an editable or virtual install rooted at ".".
func isMainPackage(p lockPackage) bool {
	return p.Source["editable"] == "." || p.Source["virtual"] == "."
}

// MainModulePath returns root's own package name (from its uv.lock entry
// whose source is editable/virtual at "."), used by callers that need to
// identify this root's own identity separately from its dependencies (e.g.
// project derivation).
func MainModulePath(root string) (string, error) {
	lf, err := parseLock(root)
	if err != nil {
		return "", err
	}
	for _, p := range lf.Package {
		if isMainPackage(p) {
			return p.Name, nil
		}
	}
	return "", fmt.Errorf("no root package (editable/virtual \".\") found in %s/uv.lock", root)
}

// expand does the real work once we know root has a uv.lock.
func expand(root string) ([]expander.Node, []expander.Edge, error) {
	lf, err := parseLock(root)
	if err != nil {
		return nil, nil, err
	}

	var mainDeps map[string]bool
	for _, p := range lf.Package {
		if isMainPackage(p) {
			mainDeps = make(map[string]bool, len(p.Dependencies))
			for _, d := range p.Dependencies {
				mainDeps[d.Name] = true
			}
			break
		}
	}

	var nodes []expander.Node
	for _, p := range lf.Package {
		if isMainPackage(p) {
			continue
		}
		nodes = append(nodes, expander.Node{
			Key:    "python:" + p.Name + "@" + p.Version,
			Kind:   "package",
			Direct: mainDeps[p.Name],
		})
	}
	return nodes, nil, nil
}
