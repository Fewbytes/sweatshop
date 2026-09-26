// Package expander defines the contract ecosystem expanders implement
// (bughunt's detector shape) and a process-wide registry. depmaint itself
// ships with zero registered expanders — Go, Node, Python, and JVM
// expanders register into this package from their own packages.
package expander

// Node is one graph node: a package or runtime at a resolved version.
type Node struct {
	Key    string // "ecosystem:name@version"
	Kind   string // "package" | "runtime"
	Direct bool
}

// Edge is one resolved dependency requirement between two nodes.
type Edge struct {
	From, To    string // node keys
	Requirement string
	Resolved    string
	Internal    bool // repo-local: workspace member or path/file: dependency
}

// Expander produces resolved truth for one ecosystem from a repo root.
type Expander interface {
	Name() string
	// Available reports whether the ecosystem's toolchain is installed.
	Available() bool
	// Expand reads the ecosystem's own resolved lockfile truth. Callers
	// only invoke this when Available() is true.
	Expand(root string) ([]Node, []Edge, error)
}

var registry []Expander

// Register adds an expander to the process-wide registry. Called from
// expander implementation packages' init(), or directly by main.
func Register(e Expander) {
	registry = append(registry, e)
}

// All returns every registered expander, in registration order.
func All() []Expander {
	out := make([]Expander, len(registry))
	copy(out, registry)
	return out
}

// resetForTest clears the registry. Test-only.
func resetForTest() {
	registry = nil
}
