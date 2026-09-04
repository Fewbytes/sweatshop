package scan

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Fewbytes/sweatshop/bughunt/internal/config"
	"github.com/Fewbytes/sweatshop/bughunt/internal/detector"
	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/store"
)

type stubDetector struct {
	name      string
	available bool
	hits      []finding.Hit
}

func (s stubDetector) Name() string                   { return s.name }
func (s stubDetector) Available(context.Context) bool { return s.available }
func (s stubDetector) Run(context.Context, string, []string) ([]finding.Hit, error) {
	return s.hits, nil
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func hit(rule, sym, text string, line int, sev finding.Severity) finding.Hit {
	return finding.Hit{RuleID: rule, File: "a.go", Line: line, Symbol: sym,
		MatchText: text, Severity: sev, Message: "m"}
}

func TestRunRecordsFindingsAndGates(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	reg := detector.NewRegistry(stubDetector{name: "govet", available: true,
		hits: []finding.Hit{hit("govet/printf", "Alpha", "x", 3, finding.SeverityMedium)}})

	res, err := Run(ctx, s, reg, config.Default(), Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(res.Findings))
	}
	if res.GatingCount != 1 || res.ExitCode() != 1 {
		t.Fatalf("GatingCount = %d, ExitCode = %d; want 1, 1", res.GatingCount, res.ExitCode())
	}
	if res.Findings[0].Status != store.StatusNew {
		t.Fatalf("status = %q, want new", res.Findings[0].Status)
	}
}

// Detectors legitimately report the same defect more than once in a single run:
// two identical `defer rows.Close()` lines inside one function share a rule id,
// an enclosing symbol, and normalized match text, so they share a fingerprint.
// Recording both would bump seen_count twice for one sighting and overstate the
// counts the gate reports.
func TestRunDedupsIdenticalFingerprintsWithinARun(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	dup := hit("errcheck/unchecked", "Alpha", "defer rows.Close()", 12, finding.SeverityMedium)
	other := dup
	other.Line = 19 // different line, same fingerprint — line is not part of identity
	reg := detector.NewRegistry(stubDetector{name: "errcheck", available: true,
		hits: []finding.Hit{dup, other}})

	res, err := Run(ctx, s, reg, config.Default(), Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("got %d findings, want 1 — the two hits share a fingerprint", len(res.Findings))
	}
	if res.GatingCount != 1 {
		t.Fatalf("GatingCount = %d, want 1", res.GatingCount)
	}
	stored, ok, err := s.GetFinding(ctx, res.Findings[0].Fingerprint)
	if err != nil || !ok {
		t.Fatalf("GetFinding: %v ok=%v", err, ok)
	}
	if stored.SeenCount != 1 {
		t.Fatalf("SeenCount = %d, want 1 — one run is one sighting", stored.SeenCount)
	}
}

func TestRunDoesNotGateOnSuppressedFinding(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	h := hit("govet/printf", "Alpha", "x", 3, finding.SeverityMedium)
	reg := detector.NewRegistry(stubDetector{name: "govet", available: true, hits: []finding.Hit{h}})
	cfg := config.Default()

	first, err := Run(ctx, s, reg, cfg, Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	fp := first.Findings[0].Fingerprint
	if err := s.SetStatus(ctx, fp, store.StatusSuppressed, "known FP"); err != nil {
		t.Fatal(err)
	}

	second, err := Run(ctx, s, reg, cfg, Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if second.GatingCount != 0 || second.ExitCode() != 0 {
		t.Fatalf("suppressed finding still gates: count = %d, exit = %d", second.GatingCount, second.ExitCode())
	}
	if len(second.Findings) != 1 {
		t.Fatal("a suppressed finding is still reported, it just does not gate")
	}
}

func TestRunAppliesSeverityFloor(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	reg := detector.NewRegistry(stubDetector{name: "govet", available: true, hits: []finding.Hit{
		hit("govet/a", "Alpha", "x", 3, finding.SeverityLow),
		hit("govet/b", "Beta", "y", 9, finding.SeverityHigh),
	}})
	cfg := config.Default()
	cfg.SeverityFloor = "high"

	res, err := Run(ctx, s, reg, cfg, Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 1 || res.Findings[0].Hit.RuleID != "govet/b" {
		t.Fatalf("severity floor not applied: %+v", res.Findings)
	}
}

func TestRunAppliesExcludes(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	excluded := hit("govet/a", "Alpha", "x", 3, finding.SeverityHigh)
	excluded.File = "vendor/dep/a.go"
	reg := detector.NewRegistry(stubDetector{name: "govet", available: true,
		hits: []finding.Hit{excluded}})
	cfg := config.Default()
	cfg.Exclude = []string{"vendor/**"}

	res, err := Run(ctx, s, reg, cfg, Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("excluded path produced findings: %+v", res.Findings)
	}
}

func TestRunRecordsUnavailableDetectors(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	reg := detector.NewRegistry(
		stubDetector{name: "govet", available: true},
		stubDetector{name: "gosec"},
	)
	res, err := Run(ctx, s, reg, config.Default(), Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("a missing detector must not fail the scan: %v", err)
	}
	if len(res.DetectorsUsed) != 1 || res.DetectorsUsed[0] != "govet" {
		t.Errorf("DetectorsUsed = %v", res.DetectorsUsed)
	}
	if len(res.DetectorsUnavailable) != 1 || res.DetectorsUnavailable[0] != "gosec" {
		t.Errorf("DetectorsUnavailable = %v", res.DetectorsUnavailable)
	}
}

func TestRunDiffModeFiltersGatingOnly(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	inDiff := hit("govet/a", "Alpha", "x", 10, finding.SeverityHigh)
	outOfDiff := hit("govet/b", "Beta", "y", 99, finding.SeverityHigh)
	reg := detector.NewRegistry(stubDetector{name: "govet", available: true,
		hits: []finding.Hit{inDiff, outOfDiff}})

	res, err := Run(ctx, s, reg, config.Default(), Options{
		Dir:          t.TempDir(),
		DiffBase:     "HEAD",
		ChangedLines: map[string][]int{"a.go": {10}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("diff mode must still record everything, got %d", len(res.Findings))
	}
	if res.GatingCount != 1 {
		t.Fatalf("GatingCount = %d, want 1 — only the finding on a changed line gates", res.GatingCount)
	}
}
