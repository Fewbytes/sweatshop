// Package pyexpander implements the depmaint Python expander: it reads
// resolved dependency truth directly from uv's own lockfile (uv.lock)
// rather than shelling out to a tool and screen-scraping text output. Never
// invokes `uv lock`/`uv sync` — those would hit the network or mutate the
// lockfile, and depmaint's expanders are read-only observers.
package pyexpander

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
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

// expand does the real work once we know root has a uv.lock. Implemented in
// Tasks 2-4.
func expand(root string) ([]expander.Node, []expander.Edge, error) {
	return nil, nil, nil
}
