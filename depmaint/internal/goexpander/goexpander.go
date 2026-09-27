// Package goexpander implements the depmaint Go expander: it reads resolved
// dependency truth from a Go module using the go toolchain's own commands
// (go list -m -json all, go mod graph) rather than parsing go.mod/go.sum by
// hand.
package goexpander

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
)

func init() {
	expander.Register(Expander{})
}

// Expander implements expander.Expander for Go modules.
type Expander struct{}

func (Expander) Name() string { return "go" }

// Available reports whether the go toolchain is installed. It says nothing
// about whether any particular root is a Go module — see Expand.
func (Expander) Available() bool {
	_, err := exec.LookPath("go")
	return err == nil
}

// Expand reads root's dependency graph via the go tool. A root with no
// go.mod is not an error — it simply contributes no nodes or edges (nil,
// nil, nil), since not every repo root is a Go module.
func (Expander) Expand(root string) ([]expander.Node, []expander.Edge, error) {
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return nil, nil, nil
	}
	return expand(root)
}

// expand does the real work once we know root has a go.mod. Implemented in
// Tasks 2-4.
func expand(root string) ([]expander.Node, []expander.Edge, error) {
	return nil, nil, nil
}
