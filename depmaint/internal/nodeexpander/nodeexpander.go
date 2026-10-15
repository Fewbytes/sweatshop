// Package nodeexpander implements the depmaint Node expander: it reads
// resolved dependency truth directly from npm's package-lock.json (lockfile
// v2/v3) rather than shelling out to `npm ls`, which can hit the network and
// whose output varies by npm version. Read-only: never runs npm.
package nodeexpander

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
)

func init() {
	expander.Register(Expander{})
}

// Expander implements expander.Expander for npm-managed Node projects.
type Expander struct{}

func (Expander) Name() string { return "node" }

// Available reports whether npm is installed. Expand only reads a static
// file, so it works regardless; this is reported for the Expander contract.
func (Expander) Available() bool {
	_, err := exec.LookPath("npm")
	return err == nil
}

// Expand reads root's dependency graph from package-lock.json. A root with
// no package-lock.json contributes no nodes or edges (nil, nil, nil).
func (Expander) Expand(root string) ([]expander.Node, []expander.Edge, error) {
	if _, err := os.Stat(filepath.Join(root, "package-lock.json")); err != nil {
		return nil, nil, nil
	}
	lf, err := parseLock(root)
	if err != nil {
		return nil, nil, err
	}
	return expand(lf)
}

// lockFile mirrors the fields we use from package-lock.json (v2/v3).
type lockFile struct {
	Packages map[string]lockPackage `json:"packages"`
}

type lockPackage struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Resolved             string            `json:"resolved"` // for link entries: lockfile path of the target
	Link                 bool              `json:"link"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
}

func parseLock(root string) (lockFile, error) {
	data, err := os.ReadFile(filepath.Join(root, "package-lock.json"))
	if err != nil {
		return lockFile{}, fmt.Errorf("reading package-lock.json: %w", err)
	}
	var lf lockFile
	if err := json.Unmarshal(data, &lf); err != nil {
		return lockFile{}, fmt.Errorf("parsing package-lock.json: %w", err)
	}
	if lf.Packages == nil {
		return lockFile{}, fmt.Errorf("package-lock.json in %s has no \"packages\" map (lockfileVersion 1 is unsupported; regenerate with npm >= 7)", root)
	}
	return lf, nil
}

// MainModulePath returns root's own package name, for callers that need
// this root's identity separately from its dependencies (project derivation).
func MainModulePath(root string) (string, error) {
	lf, err := parseLock(root)
	if err != nil {
		return "", err
	}
	name := lf.Packages[""].Name
	if name == "" {
		return "", fmt.Errorf("no root package name in %s/package-lock.json", root)
	}
	return name, nil
}

// packageName is the package's real name: the entry's own "name" field
// (set for aliases and workspace members), else the last node_modules
// segment of its lockfile path.
func packageName(lockPath string, p lockPackage) string {
	if p.Name != "" {
		return p.Name
	}
	return lockPath[strings.LastIndex(lockPath, "node_modules/")+len("node_modules/"):]
}

// isInternal reports whether the entry at lockPath is repo-local (a
// workspace member or file: dependency): such packages live outside
// node_modules once links are followed.
func isInternal(lockPath string) bool {
	return !strings.Contains(lockPath, "node_modules/")
}

// lookup resolves dependency name from the package at fromPath using
// node's resolution order: nearest enclosing node_modules first, up to the
// root. A link entry is followed to the directory it points at.
func lookup(lf lockFile, fromPath, name string) (string, lockPackage, bool) {
	dir := fromPath
	for {
		candidate := path.Join(dir, "node_modules", name)
		if p, ok := lf.Packages[candidate]; ok {
			if p.Link {
				target, ok := lf.Packages[p.Resolved]
				return p.Resolved, target, ok
			}
			return candidate, p, true
		}
		if dir == "" {
			return "", lockPackage{}, false
		}
		if i := strings.LastIndex(dir, "node_modules/"); i >= 0 {
			dir = strings.TrimSuffix(dir[:i], "/")
		} else {
			dir = ""
		}
	}
}

func nodeKey(name, version string) string {
	return "node:" + name + "@" + version
}

func sortedNames(deps map[string]string) []string {
	names := make([]string, 0, len(deps))
	for name := range deps {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func expand(lf lockFile) ([]expander.Node, []expander.Edge, error) {
	rootPkg := lf.Packages[""]
	direct := make(map[string]bool)
	for _, deps := range []map[string]string{rootPkg.Dependencies, rootPkg.DevDependencies, rootPkg.OptionalDependencies} {
		for name := range deps {
			direct[name] = true
		}
	}

	// Link entries are aliases of their target entry; only targets become nodes.
	var lockPaths []string
	for lockPath, p := range lf.Packages {
		if lockPath != "" && !p.Link {
			lockPaths = append(lockPaths, lockPath)
		}
	}
	sort.Strings(lockPaths)

	var nodes []expander.Node
	seen := make(map[string]bool)
	for _, lockPath := range lockPaths {
		p := lf.Packages[lockPath]
		name := packageName(lockPath, p)
		key := nodeKey(name, p.Version)
		if seen[key] {
			continue
		}
		seen[key] = true
		// Only the hoisted top-level copy can be what the root asked for.
		topLevel := !strings.Contains(strings.TrimPrefix(lockPath, "node_modules/"), "node_modules/")
		nodes = append(nodes, expander.Node{Key: key, Kind: "package", Direct: direct[name] && topLevel})
	}

	var edges []expander.Edge
	addEdges := func(fromPath string, fromKey string, deps map[string]string, optional bool) error {
		for _, name := range sortedNames(deps) {
			toPath, to, ok := lookup(lf, fromPath, name)
			if !ok {
				if optional {
					continue // not installed on this platform
				}
				return fmt.Errorf("%s requires %s but package-lock.json has no entry for it", fromKey, name)
			}
			edges = append(edges, expander.Edge{
				From:        fromKey,
				To:          nodeKey(packageName(toPath, to), to.Version),
				Requirement: deps[name],
				Resolved:    to.Version,
				Internal:    isInternal(toPath),
			})
		}
		return nil
	}

	rootKey := nodeKey(rootPkg.Name, rootPkg.Version)
	for _, group := range []struct {
		deps     map[string]string
		optional bool
	}{{rootPkg.Dependencies, false}, {rootPkg.DevDependencies, false}, {rootPkg.OptionalDependencies, true}} {
		if err := addEdges("", rootKey, group.deps, group.optional); err != nil {
			return nil, nil, err
		}
	}
	for _, lockPath := range lockPaths {
		p := lf.Packages[lockPath]
		fromKey := nodeKey(packageName(lockPath, p), p.Version)
		if err := addEdges(lockPath, fromKey, p.Dependencies, false); err != nil {
			return nil, nil, err
		}
		if err := addEdges(lockPath, fromKey, p.OptionalDependencies, true); err != nil {
			return nil, nil, err
		}
	}
	return nodes, edges, nil
}
