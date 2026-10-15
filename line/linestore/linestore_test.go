package linestore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/report"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(context.Background(), filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func TestRunsRoundTripOldestFirst(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	started := time.Date(2026, 10, 15, 12, 0, 0, 5, time.UTC)
	want := []report.Stats{
		{RunID: "a/1", Commit: "a", Tree: "t1", Role: "maker", AgentID: "m1", Runner: "r", StartedAt: started, DurationMS: 1500,
			Checks: 2, Failures: 1, Outcome: report.RunFailed, ReportKey: "reports/a/junit.xml", HTMLKey: "reports/a/report.html"},
		{RunID: "b/2", Commit: "b", StartedAt: started.Add(time.Minute), Checks: 2, Passed: true, Outcome: report.RunPassed},
	}
	for _, st := range want {
		if err := s.RecordRun(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Runs(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("runs = %+v, %v; want %+v", got, err, want)
	}
	if ok, err := s.CommitPassed(ctx, "b"); err != nil || !ok {
		t.Fatalf("CommitPassed(b) = %t, %v", ok, err)
	}
	if ok, err := s.CommitPassed(ctx, "a"); err != nil || ok {
		t.Fatalf("CommitPassed(a) = %t, %v; a failed", ok, err)
	}
}

func TestTestsRoundTripOldestFirst(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	started := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	want := []report.TestRow{
		{RunID: "a/1", Commit: "a", Tree: "t", Role: "maker", StartedAt: started, Check: "ci", Package: "p", Test: "TestX", Outcome: report.TestFailed, DurationMS: 7},
		{RunID: "a/1", Commit: "a", Tree: "t", Role: "maker", StartedAt: started, Check: "ci", Package: "p", Test: "TestY", Outcome: report.TestPassed},
	}
	if err := s.RecordTests(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Tests(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("tests = %+v, %v; want %+v", got, err, want)
	}
}

func TestKaizenBaselinePerCommitAndPreviousSkipsOwnCommit(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if prev, err := s.PreviousKaizenBaseline(ctx, "b"); err != nil || prev != nil {
		t.Fatalf("empty history: %+v, %v", prev, err)
	}
	for _, b := range []KaizenBaseline{
		{Commit: "a", Signals: json.RawMessage(`{"n":1}`)},
		{Commit: "b", Signals: json.RawMessage(`{"n":2}`)},
		{Commit: "b", Signals: json.RawMessage(`{"n":3}`)}, // published again after the change: ignored
	} {
		if err := s.AddKaizenBaseline(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	prev, err := s.PreviousKaizenBaseline(ctx, "b")
	if err != nil || prev == nil || prev.Commit != "a" || string(prev.Signals) != `{"n":1}` {
		t.Fatalf("previous of b: %+v, %v", prev, err)
	}
	prev, err = s.PreviousKaizenBaseline(ctx, "a")
	if err != nil || prev == nil || prev.Commit != "b" || string(prev.Signals) != `{"n":2}` {
		t.Fatalf("previous of a: %+v, %v; want b's first baseline", prev, err)
	}
}

func TestLevelsDefaultToZeroAndLatestWins(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if l, err := s.Levels(ctx); err != nil || l != (Levels{}) {
		t.Fatalf("unset levels = %+v, %v", l, err)
	}
	for _, l := range []Levels{{Agents: 1, WIPLimit: 2}, {Agents: 3, WIPLimit: 6}} {
		if err := s.SetLevels(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	if l, err := s.Levels(ctx); err != nil || l != (Levels{Agents: 3, WIPLimit: 6}) {
		t.Fatalf("levels = %+v, %v", l, err)
	}
}

func TestStateSurvivesReopen(t *testing.T) {
	s, dir := openTemp(t)
	ctx := context.Background()
	if err := s.SetLevels(ctx, Levels{Agents: 2, WIPLimit: 4}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if l, err := s.Levels(ctx); err != nil || l != (Levels{Agents: 2, WIPLimit: 4}) {
		t.Fatalf("levels after reopen = %+v, %v", l, err)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestImportLegacyCopiesEachFileOnce(t *testing.T) {
	s, dir := openTemp(t)
	ctx := context.Background()
	writeFile(t, dir, "runs.jsonl", `{"RunID":"a/1","Commit":"a","StartedAt":"2026-10-15T12:00:00Z","Passed":true}`+"\n"+`{torn`+"\n")
	writeFile(t, dir, "tests.jsonl", `{"run_id":"a/1","commit":"a","started_at":"2026-10-15T12:00:00Z","package":"p","test":"TestX","outcome":"pass"}`+"\n")
	writeFile(t, dir, "kaizen-signals.jsonl", `{"commit":"a","signals":{"n":1}}`+"\n"+`{"commit":"a","signals":{"n":9}}`+"\n")
	writeFile(t, dir, "heijunka.json", `{"agents":2,"wip_limit":4}`+"\n")
	for range 2 { // the second import is the server restarting
		if err := s.ImportLegacy(ctx, dir); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.Runs(ctx)
	if err != nil || len(runs) != 1 || runs[0].Commit != "a" || !runs[0].Passed {
		t.Fatalf("runs = %+v, %v; want the one intact line, once", runs, err)
	}
	if tests, err := s.Tests(ctx); err != nil || len(tests) != 1 || tests[0].Test != "TestX" {
		t.Fatalf("tests = %+v, %v", tests, err)
	}
	if prev, err := s.PreviousKaizenBaseline(ctx, "z"); err != nil || prev == nil || string(prev.Signals) != `{"n":1}` {
		t.Fatalf("baseline = %+v, %v; want the first record of the commit", prev, err)
	}
	if l, err := s.Levels(ctx); err != nil || l != (Levels{Agents: 2, WIPLimit: 4}) {
		t.Fatalf("levels = %+v, %v", l, err)
	}
	writeFile(t, dir, "runs.jsonl", `{"RunID":"b/2","Commit":"b"}`+"\n")
	if err := s.ImportLegacy(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if runs, _ := s.Runs(ctx); len(runs) != 1 {
		t.Fatalf("a file edited after its import was imported again: %+v", runs)
	}
}

func TestImportLegacyWithoutFilesIsANoOp(t *testing.T) {
	s, dir := openTemp(t)
	if err := s.ImportLegacy(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
}
