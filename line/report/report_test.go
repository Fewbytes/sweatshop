package report

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type memObjects map[string][]byte

func (m memObjects) Put(_ context.Context, key, _ string, data []byte) error {
	m[key] = data
	return nil
}

type memRuns []Stats

func (m *memRuns) RecordRun(_ context.Context, s Stats) error { *m = append(*m, s); return nil }

type failRuns struct{}

func (failRuns) RecordRun(context.Context, Stats) error { return errors.New("db down") }

func sample() Run {
	t0 := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	return Run{
		Commit: strings.Repeat("a", 40), Runner: "host", StartedAt: t0, FinishedAt: t0.Add(3500 * time.Millisecond),
		Checks: []Check{
			{Name: "lint", Command: "just lint", Duration: time.Second},
			{Name: "test", Command: "just test", ExitCode: 1, Duration: 2 * time.Second, Output: "<boom>\x00 & more"},
		},
	}
}

func TestJUnitXMLIsWellFormed(t *testing.T) {
	data, err := JUnitXML(sample())
	if err != nil {
		t.Fatal(err)
	}
	var doc testsuites
	if err := xml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("not well-formed: %v\n%s", err, data)
	}
	if doc.Tests != 2 || doc.Failures != 1 || doc.Suite[0].Cases[1].Failure == nil {
		t.Fatalf("unexpected counts: %+v", doc)
	}
	if !strings.Contains(doc.Suite[0].Cases[1].Failure.Body, "<boom> & more") {
		t.Fatalf("failure body lost: %q", doc.Suite[0].Cases[1].Failure.Body)
	}
}

func TestHTMLEscapesOutput(t *testing.T) {
	page := HTML(sample())
	if !bytes.Contains(page, []byte("FAILED")) || bytes.Contains(page, []byte("<boom>")) {
		t.Fatalf("bad page: %s", page)
	}
}

func TestPublishStoresReportsAndStats(t *testing.T) {
	objs, runs := memObjects{}, &memRuns{}
	if err := (Reporter{Objects: objs, Runs: runs}).Publish(context.Background(), sample()); err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 || len(*runs) != 1 {
		t.Fatalf("objects=%d runs=%d", len(objs), len(*runs))
	}
	s := (*runs)[0]
	if s.DurationMS != 3500 || s.Checks != 2 || s.Failures != 1 || s.Passed {
		t.Fatalf("stats: %+v", s)
	}
	if _, ok := objs[s.ReportKey]; !ok {
		t.Fatalf("report key %q not stored", s.ReportKey)
	}
	if _, ok := objs[s.HTMLKey]; !ok {
		t.Fatalf("html key %q not stored", s.HTMLKey)
	}
}

func TestPublishReportsErrorsButStillRecords(t *testing.T) {
	if err := (Reporter{Runs: failRuns{}}).Publish(context.Background(), sample()); err == nil {
		t.Fatal("want error")
	}
}

func TestFSStore(t *testing.T) {
	dir := t.TempDir()
	s := FSStore{Root: dir}
	if err := s.Put(context.Background(), "reports/x/junit.xml", "application/xml", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "reports/x/junit.xml")); string(got) != "hi" {
		t.Fatalf("got %q", got)
	}
	for _, key := range []string{"../escape", "/abs", "a/../../b"} {
		if err := s.Put(context.Background(), key, "", nil); err == nil {
			t.Errorf("key %q accepted", key)
		}
	}
}

func TestJSONLRunStoreAppends(t *testing.T) {
	s := &JSONLRunStore{Path: filepath.Join(t.TempDir(), "sub", "runs.jsonl")}
	for i := 0; i < 2; i++ {
		if err := s.RecordRun(context.Background(), Stats{Commit: "abc", Checks: i}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "\n"); n != 2 {
		t.Fatalf("want 2 lines, got %d: %q", n, b)
	}
}

const goTestJSON = `lint noise
{"Action":"run","Package":"p","Test":"TestA"}
{"Action":"output","Package":"p","Test":"TestA","Output":"x"}
{"Action":"pass","Package":"p","Test":"TestA","Elapsed":0.25}
{"Action":"fail","Package":"p","Test":"TestB","Elapsed":1.5}
{"Action":"skip","Package":"p","Test":"TestC","Elapsed":0}
{"Action":"fail","Package":"p","Elapsed":2}
{"Action":"pass","Pack
`

func TestParseGoTestJSONKeepsOnlyTestVerdicts(t *testing.T) {
	got := ParseGoTestJSON([]byte(goTestJSON))
	want := []TestResult{
		{Package: "p", Name: "TestA", Outcome: TestPassed, Duration: 250 * time.Millisecond},
		{Package: "p", Name: "TestB", Outcome: TestFailed, Duration: 1500 * time.Millisecond},
		{Package: "p", Name: "TestC", Outcome: TestSkipped},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("result %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestPublishRecordsPerTestRowsAndJUnitCases(t *testing.T) {
	r := sample()
	r.Checks[1].Tests = ParseGoTestJSON([]byte(goTestJSON))
	objs, runs := memObjects{}, &memRuns{}
	tests := &JSONLTestStore{Path: filepath.Join(t.TempDir(), "tests.jsonl")}
	if err := (Reporter{Objects: objs, Runs: runs, Tests: tests}).Publish(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	rows, err := ReadTestsJSONL(tests.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[1].Test != "TestB" || rows[1].Outcome != TestFailed || rows[1].DurationMS != 1500 ||
		rows[1].RunID != (*runs)[0].RunID || rows[1].Commit != r.Commit || rows[1].Check != "test" {
		t.Fatalf("rows = %+v, run = %+v", rows, (*runs)[0])
	}
	xmlData := string(objs[(*runs)[0].ReportKey])
	for _, want := range []string{`name="TestB" classname="p"`, "<skipped>", `name="test tests"`} {
		if !strings.Contains(xmlData, want) {
			t.Errorf("junit lacks %q:\n%s", want, xmlData)
		}
	}
}

func TestInfraRunIsNeitherPassNorTestFailure(t *testing.T) {
	r := sample()
	r.Infra = "run check test: timed out after 1h"
	runs := &memRuns{}
	objs := memObjects{}
	if err := (Reporter{Objects: objs, Runs: runs}).Publish(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	s := (*runs)[0]
	if s.Passed || s.Outcome != RunInfra || s.Result() != RunInfra {
		t.Fatalf("stats = %+v", s)
	}
	if xmlData := string(objs[s.ReportKey]); !strings.Contains(xmlData, "<error") || !strings.Contains(xmlData, "timed out") {
		t.Fatalf("junit has no <error> for the infra failure:\n%s", xmlData)
	}
}

func TestStatsResultDerivesOutcomeForRowsSavedBeforeOutcomeExisted(t *testing.T) {
	if (Stats{Passed: true}).Result() != RunPassed || (Stats{}).Result() != RunFailed {
		t.Fatal("legacy rows must map Passed to passed and !Passed to failed")
	}
}
