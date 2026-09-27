// Package goexpander implements the depmaint Go expander: it reads resolved
// dependency truth from a Go module using the go toolchain's own commands
// (go list -m -json all, go mod graph) rather than parsing go.mod/go.sum by
// hand.
package goexpander

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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

// modGraph runs `go mod graph` in root and returns each edge as a
// (requirer, required) pair of raw tokens exactly as printed — each token is
// either "path@version" or, for the main module, bare "path" with no "@".
func modGraph(root string) ([][2]string, error) {
	cmd := exec.Command("go", "mod", "graph")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go mod graph: %w", err)
	}
	var pairs [][2]string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		pairs = append(pairs, [2]string{fields[0], fields[1]})
	}
	return pairs, scanner.Err()
}

// splitToken splits a `go mod graph` token into (path, version). The main
// module's token has no "@" and thus an empty version.
func splitToken(tok string) (path, version string) {
	if i := strings.LastIndex(tok, "@"); i >= 0 {
		return tok[:i], tok[i+1:]
	}
	return tok, ""
}

// isLocalPath reports whether a replace directive's target looks like a
// filesystem path rather than a module path fetched from a proxy/registry —
// i.e. it starts with "." or is absolute. Go's own rule (see `go help
// goproxy`) is exactly this: a replacement path containing a "/" and
// starting with "./" or "../", or an absolute path, is a filesystem
// replacement; anything else is a module-path replacement (still an
// external dependency, just pinned to a different version/fork).
func isLocalPath(p string) bool {
	return filepath.IsAbs(p) || strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../")
}

// expand does the real work once we know root has a go.mod. Implemented in
// Tasks 2-4.
func expand(root string) ([]expander.Node, []expander.Edge, error) {
	mods, err := listModules(root)
	if err != nil {
		return nil, nil, err
	}

	resolved := make(map[string]string, len(mods)) // module path -> final resolved version
	internal := make(map[string]bool, len(mods))
	var nodes []expander.Node
	for _, m := range mods {
		if m.Main {
			continue
		}
		version := m.Version
		if m.Replace != nil && isLocalPath(m.Replace.Path) {
			internal[m.Path] = true
			if version == "" {
				version = "(local)"
			}
		} else if version == "" {
			version = "(replaced)"
		}
		resolved[m.Path] = version
		nodes = append(nodes, expander.Node{
			Key:    "go:" + m.Path + "@" + version,
			Kind:   "package",
			Direct: !m.Indirect,
		})
	}

	pairs, err := modGraph(root)
	if err != nil {
		return nil, nil, err
	}
	var edges []expander.Edge
	for _, p := range pairs {
		fromPath, fromVer := splitToken(p[0])
		toPath, toVer := splitToken(p[1])
		// "go" and "toolchain" are Go's reserved pseudo-modules tracking the
		// language/toolchain version requirement (present in `go mod graph`
		// since Go 1.21, even for a module with zero real dependencies) —
		// they are not packages and never appear in `go list -m -json all`,
		// so they must never become graph nodes/edges.
		if fromPath == "go" || fromPath == "toolchain" || toPath == "go" || toPath == "toolchain" {
			continue
		}
		toResolved := resolved[toPath]
		if toResolved == "" {
			toResolved = toVer
		}
		edges = append(edges, expander.Edge{
			From:        "go:" + fromPath + "@" + fromVer,
			To:          "go:" + toPath + "@" + toResolved,
			Requirement: toVer,
			Resolved:    toResolved,
			Internal:    internal[toPath],
		})
	}
	return nodes, edges, nil
}
