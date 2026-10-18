package enrich

import (
	"testing"
	"time"
)

func TestCycleForPicksLongestDotBoundaryPrefix(t *testing.T) {
	cycles, err := ParseCycles([]byte(`[
	  {"cycle":"1","eol":true},
	  {"cycle":"1.2","eol":"2024-01-01"},
	  {"cycle":"1.20","eol":false}]`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for version, want := range map[string]struct {
		cycle string
		eol   bool
	}{
		"1.2.5":  {"1.2", true},
		"1.20.1": {"1.20", false},
		"1.9.0":  {"1", true},
	} {
		c, ok := CycleFor(cycles, version)
		if !ok || c.Name != want.cycle || c.IsEOL(now) != want.eol {
			t.Errorf("CycleFor(%s) = %+v ok=%v, want cycle %s eol=%v", version, c, ok, want.cycle, want.eol)
		}
	}
	if _, ok := CycleFor(cycles, "2.0.0"); ok {
		t.Error("version 2.0.0 matched a cycle")
	}
}

func TestParseCyclesRejectsMalformedEOL(t *testing.T) {
	if _, err := ParseCycles([]byte(`[{"cycle":"1","eol":"soon"}]`)); err == nil {
		t.Fatal("want error for non-date eol")
	}
	if _, err := ParseCycles([]byte(`[{"cycle":"1","eol":7}]`)); err == nil {
		t.Fatal("want error for numeric eol")
	}
}
