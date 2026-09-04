package detector

import (
	"context"
	"testing"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
)

type fakeDetector struct {
	name      string
	available bool
}

func (f fakeDetector) Name() string                   { return f.name }
func (f fakeDetector) Available(context.Context) bool { return f.available }
func (f fakeDetector) Run(context.Context, string, []string) ([]finding.Hit, error) {
	return nil, nil
}

type fakeConfig map[string]bool

func (c fakeConfig) DetectorEnabled(name string) bool {
	enabled, ok := c[name]
	return !ok || enabled
}

func TestRegistryEnabledRespectsConfig(t *testing.T) {
	r := NewRegistry(fakeDetector{name: "govet"}, fakeDetector{name: "gosec"})
	got := r.Enabled(fakeConfig{"gosec": false})
	if len(got) != 1 || got[0].Name() != "govet" {
		t.Fatalf("Enabled returned %v, want [govet]", names(got))
	}
}

func TestRegistryPartitionSplitsOnAvailability(t *testing.T) {
	r := NewRegistry()
	dets := []Detector{fakeDetector{name: "here", available: true}, fakeDetector{name: "gone"}}
	available, unavailable := r.Partition(context.Background(), dets)
	if len(available) != 1 || available[0].Name() != "here" {
		t.Fatalf("available = %v, want [here]", names(available))
	}
	if len(unavailable) != 1 || unavailable[0].Name() != "gone" {
		t.Fatalf("unavailable = %v, want [gone]", names(unavailable))
	}
}

func names(dets []Detector) []string {
	out := make([]string, len(dets))
	for i, d := range dets {
		out[i] = d.Name()
	}
	return out
}
