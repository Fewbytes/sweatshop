// Package goexpander implements the depmaint Go expander: it reads resolved
// dependency truth from a Go module using the go toolchain's own commands
// (go list -m -json all, go mod graph) rather than parsing go.mod/go.sum by
// hand.
package goexpander

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// goModule mirrors the fields we use from `go list -m -json` output.
type goModule struct {
	Path     string
	Version  string
	Main     bool
	Indirect bool
	Replace  *goModule
}

// listModules runs `go list -m -json all` in root and decodes the stream of
// JSON objects it prints (one per module, not wrapped in an array).
func listModules(root string) ([]goModule, error) {
	cmd := exec.Command("go", "list", "-m", "-json", "all")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list -m -json all: %w", err)
	}
	var mods []goModule
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var m goModule
		if err := dec.Decode(&m); err != nil {
			return nil, fmt.Errorf("decoding go list output: %w", err)
		}
		mods = append(mods, m)
	}
	return mods, nil
}

// MainModulePath returns root's own module path (the module declared in its
// go.mod), used by callers that need to identify this root's own identity
// separately from its dependencies (e.g. project derivation).
func MainModulePath(root string) (string, error) {
	mods, err := listModules(root)
	if err != nil {
		return "", err
	}
	for _, m := range mods {
		if m.Main {
			return m.Path, nil
		}
	}
	return "", fmt.Errorf("no main module found in %s", root)
}

// expand does the real work once we know root has a go.mod. Implemented in
// Tasks 2-4.
func expand(root string) ([]expander.Node, []expander.Edge, error) {
	mods, err := listModules(root)
	if err != nil {
		return nil, nil, err
	}

	var nodes []expander.Node
	for _, m := range mods {
		if m.Main {
			continue
		}
		version := m.Version
		if version == "" {
			version = "(replaced)"
		}
		nodes = append(nodes, expander.Node{
			Key:    "go:" + m.Path + "@" + version,
			Kind:   "package",
			Direct: !m.Indirect,
		})
	}
	return nodes, nil, nil
}
