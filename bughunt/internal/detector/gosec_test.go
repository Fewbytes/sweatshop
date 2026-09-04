package detector

import (
	"context"
	"testing"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

const gosecJSON = `{
  "Issues": [
    {
      "severity": "HIGH",
      "confidence": "HIGH",
      "rule_id": "G404",
      "details": "Use of weak random number generator",
      "file": "/tmp/x/a.go",
      "line": "17",
      "column": "9",
      "code": "rand.Intn(10)"
    }
  ]
}`

func TestGosecParsesJSON(t *testing.T) {
	hits, err := NewGosec(fakeRunner{stdout: gosecJSON, exitCode: 1}, symbol.NewGo()).
		Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	h := hits[0]
	if h.RuleID != "gosec/G404" {
		t.Errorf("RuleID = %q, want gosec/G404", h.RuleID)
	}
	if h.Line != 17 {
		t.Errorf("Line = %d, want 17", h.Line)
	}
	if h.Severity != finding.SeverityHigh {
		t.Errorf("Severity = %q, want high", h.Severity)
	}
	if h.MatchText != "rand.Intn(10)" {
		t.Errorf("MatchText = %q, want the code field", h.MatchText)
	}
}

func TestGosecEmptyOutputIsNotAnError(t *testing.T) {
	hits, err := NewGosec(fakeRunner{}, symbol.NewGo()).Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("got %d hits, want 0", len(hits))
	}
}

func TestGosecMalformedJSONIsAnError(t *testing.T) {
	_, err := NewGosec(fakeRunner{stdout: "{not json"}, symbol.NewGo()).
		Run(context.Background(), t.TempDir(), nil)
	if err == nil {
		t.Fatal("malformed detector output must surface as an error, not silence")
	}
}
