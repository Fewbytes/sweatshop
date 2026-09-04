package detector

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// ast-grep --json emits an array of matches.
const astGrepJSON = `[
  {
    "ruleId": "no-time-now-in-handler",
    "severity": "warning",
    "message": "call to time.Now inside a handler",
    "file": "internal/api/handler.go",
    "range": {"start": {"line": 41, "column": 8}, "end": {"line": 41, "column": 20}},
    "text": "time.Now()"
  }
]`

func rulesDirWith(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("id: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestAstGrepParsesJSONAndConvertsToOneIndexedLines(t *testing.T) {
	d := NewAstGrep(fakeRunner{stdout: astGrepJSON}, symbol.NewGo(), rulesDirWith(t, "r.yaml"))
	hits, err := d.Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	h := hits[0]
	if h.RuleID != "astgrep/no-time-now-in-handler" {
		t.Errorf("RuleID = %q", h.RuleID)
	}
	if h.Line != 42 {
		t.Errorf("Line = %d, want 42 — ast-grep lines are 0-indexed", h.Line)
	}
	if h.MatchText != "time.Now()" {
		t.Errorf("MatchText = %q, want time.Now()", h.MatchText)
	}
	if h.Severity != finding.SeverityMedium {
		t.Errorf("Severity = %q, want medium for warning", h.Severity)
	}
}

func TestAstGrepUnavailableWithoutRules(t *testing.T) {
	d := NewAstGrep(fakeRunner{}, symbol.NewGo(), rulesDirWith(t))
	if d.Available(context.Background()) {
		t.Fatal("no rule files means the adapter has nothing to do")
	}
}

func TestAstGrepUnavailableWhenRulesDirMissing(t *testing.T) {
	d := NewAstGrep(fakeRunner{}, symbol.NewGo(), filepath.Join(t.TempDir(), "absent"))
	if d.Available(context.Background()) {
		t.Fatal("missing rules dir must not be available")
	}
}

func TestAstGrepEmptyOutputIsNotAnError(t *testing.T) {
	d := NewAstGrep(fakeRunner{}, symbol.NewGo(), rulesDirWith(t, "r.yaml"))
	hits, err := d.Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("got %d hits, want 0", len(hits))
	}
}
