package detector

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// configAndRulesDir creates a parent temp directory with a rules subdirectory
// and an sgconfig.yml that references it. It writes yaml files to the rules dir.
// Returns (configPath, rulesDir).
func configAndRulesDir(t *testing.T, ruleFiles ...string) (string, string) {
	t.Helper()
	parentDir := t.TempDir()
	rulesDir := filepath.Join(parentDir, "rules")
	if err := os.Mkdir(rulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range ruleFiles {
		if err := os.WriteFile(filepath.Join(rulesDir, name), []byte("id: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(parentDir, "sgconfig.yml")
	configContent := "ruleDirs:\n  - rules\n"
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatal(err)
	}
	return configPath, rulesDir
}

func TestAstGrepParsesJSONAndConvertsToOneIndexedLines(t *testing.T) {
	cfgPath, rulesDir := configAndRulesDir(t, "r.yaml")
	var got []string
	runner := fakeRunner{stdout: astGrepJSON, gotArgs: &got}
	d := NewAstGrep(runner, symbol.NewGo(), rulesDir, cfgPath)
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

	// Verify args passed to runner contain -c and config path, but NOT --rule-dir
	if len(got) == 0 {
		t.Fatal("expected runner args to be populated")
	}
	argsStr := strings.Join(got, " ")
	if !strings.Contains(argsStr, "-c") {
		t.Errorf("args missing -c flag: %v", got)
	}
	if !strings.Contains(argsStr, cfgPath) {
		t.Errorf("args missing config path %q: %v", cfgPath, got)
	}
	if strings.Contains(argsStr, "--rule-dir") {
		t.Errorf("args should NOT contain --rule-dir: %v", got)
	}
}

func TestAstGrepUnavailableWithoutRules(t *testing.T) {
	cfgPath, rulesDir := configAndRulesDir(t)
	d := NewAstGrep(fakeRunner{}, symbol.NewGo(), rulesDir, cfgPath)
	if d.Available(context.Background()) {
		t.Fatal("no rule files means the adapter has nothing to do")
	}
}

func TestAstGrepUnavailableWhenRulesDirMissing(t *testing.T) {
	absentDir := filepath.Join(t.TempDir(), "absent")
	cfgPath := filepath.Join(t.TempDir(), "sgconfig.yml")
	d := NewAstGrep(fakeRunner{}, symbol.NewGo(), absentDir, cfgPath)
	if d.Available(context.Background()) {
		t.Fatal("missing rules dir must not be available")
	}
}

func TestAstGrepUnavailableWhenConfigMissing(t *testing.T) {
	_, rulesDir := configAndRulesDir(t, "r.yaml")
	// Use a different config path that doesn't exist
	missingCfgPath := filepath.Join(t.TempDir(), "missing.yml")
	d := NewAstGrep(fakeRunner{}, symbol.NewGo(), rulesDir, missingCfgPath)
	if d.Available(context.Background()) {
		t.Fatal("missing config file means the adapter is unavailable")
	}
}

func TestAstGrepEmptyOutputIsNotAnError(t *testing.T) {
	cfgPath, rulesDir := configAndRulesDir(t, "r.yaml")
	d := NewAstGrep(fakeRunner{}, symbol.NewGo(), rulesDir, cfgPath)
	hits, err := d.Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("got %d hits, want 0", len(hits))
	}
}
