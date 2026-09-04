package store

import (
	"context"
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := open(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

func TestUpsertFindingInsertsAsNew(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	f := Finding{Fingerprint: "abc", RuleID: "govet/printf", Detector: "govet", File: "a.go", Line: 10, FirstSeenRun: "r1", LastSeenRun: "r1"}
	got, err := s.UpsertFinding(ctx, f)
	if err != nil {
		t.Fatalf("UpsertFinding: %v", err)
	}
	if got != StatusNew {
		t.Fatalf("status = %q, want new", got)
	}
	stored, ok, err := s.GetFinding(ctx, "abc")
	if err != nil || !ok {
		t.Fatalf("GetFinding: %v ok=%v", err, ok)
	}
	if stored.SeenCount != 1 {
		t.Fatalf("SeenCount = %d, want 1", stored.SeenCount)
	}
}

func TestUpsertFindingPreservesStatusAndBumpsSeenCount(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	f := Finding{Fingerprint: "abc", RuleID: "r", Detector: "govet", File: "a.go", Line: 10, FirstSeenRun: "r1", LastSeenRun: "r1"}
	if _, err := s.UpsertFinding(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(ctx, "abc", StatusSuppressed, "known false positive"); err != nil {
		t.Fatal(err)
	}

	f.Line = 42
	f.LastSeenRun = "r2"
	got, err := s.UpsertFinding(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if got != StatusSuppressed {
		t.Fatalf("status = %q, want suppressed — upsert must not resurrect a triaged finding", got)
	}
	stored, _, err := s.GetFinding(ctx, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if stored.SeenCount != 2 {
		t.Fatalf("SeenCount = %d, want 2", stored.SeenCount)
	}
	if stored.Line != 42 {
		t.Fatalf("Line = %d, want 42 — line is display data and must track the latest sighting", stored.Line)
	}
	if stored.FirstSeenRun != "r1" {
		t.Fatalf("FirstSeenRun = %q, want r1", stored.FirstSeenRun)
	}
}

func TestStatusGates(t *testing.T) {
	gating := []Status{StatusNew, StatusConfirmed}
	quiet := []Status{StatusFixed, StatusUnreproducible, StatusSuppressed, StatusEscalated}
	for _, s := range gating {
		if !s.Gates() {
			t.Errorf("%q should gate", s)
		}
	}
	for _, s := range quiet {
		if s.Gates() {
			t.Errorf("%q should not gate", s)
		}
	}
}

func TestInsertRunAndList(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	r := Run{ID: "r1", StartedAt: "2026-08-23T00:00:00Z", CommitSHA: "deadbeef", Mode: "scan",
		DetectorsUsed: []string{"govet"}, DetectorsUnavailable: []string{"gosec"}, FindingCount: 1, NewCount: 1}
	if err := s.InsertRun(ctx, r); err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	if _, err := s.UpsertFinding(ctx, Finding{Fingerprint: "x", RuleID: "r", Detector: "govet", File: "a.go", FirstSeenRun: "r1", LastSeenRun: "r1"}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListFindings(ctx, "")
	if err != nil || len(all) != 1 {
		t.Fatalf("ListFindings: %v len=%d", err, len(all))
	}
	none, err := s.ListFindings(ctx, StatusFixed)
	if err != nil || len(none) != 0 {
		t.Fatalf("ListFindings(fixed): %v len=%d", err, len(none))
	}
}
