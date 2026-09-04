// Package detector adapts external analysis tools to one normalized interface.
// Adding a language means adding adapters here and nothing else.
package detector

import (
	"context"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
)

// Detector wraps one analysis tool.
type Detector interface {
	// Name is the stable identifier used in config and in rule id prefixes.
	Name() string
	// Available reports whether the underlying tool is installed and usable.
	Available(ctx context.Context) bool
	// Run analyses paths within dir. Empty paths means the whole tree.
	Run(ctx context.Context, dir string, paths []string) ([]finding.Hit, error)
}

// Registry holds the detectors compiled into this binary.
type Registry struct{ dets []Detector }

// NewRegistry builds a registry over dets.
func NewRegistry(dets ...Detector) *Registry { return &Registry{dets: dets} }

// All returns every registered detector.
func (r *Registry) All() []Detector { return r.dets }

// Enabled filters the registry by configuration.
func (r *Registry) Enabled(cfg interface{ DetectorEnabled(string) bool }) []Detector {
	var out []Detector
	for _, d := range r.dets {
		if cfg.DetectorEnabled(d.Name()) {
			out = append(out, d)
		}
	}
	return out
}

// Partition splits detectors by whether their tool is installed. A missing tool
// is reported, never fatal: a gate that fails because a linter is not installed
// gets disabled by the first person it annoys.
func (r *Registry) Partition(ctx context.Context, dets []Detector) (available, unavailable []Detector) {
	for _, d := range dets {
		if d.Available(ctx) {
			available = append(available, d)
		} else {
			unavailable = append(unavailable, d)
		}
	}
	return available, unavailable
}
