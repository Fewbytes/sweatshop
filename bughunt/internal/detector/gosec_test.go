package detector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// gosecJSONTemplate is a real `gosec -fmt=json -quiet ./...` capture (with the
// file path substituted at test time) against a temp module containing:
//
//	func main() {
//	        x := 1
//	        y := 2
//	        z := 3
//	        fmt.Println(rand.Intn(10), x, y, z)
//	}
//
// Note "code" is a three-line context window with "NNN: " line-number
// prefixes baked in — that is the shape MatchText must NOT be derived from.
const gosecJSONTemplate = `{
  "Issues": [
    {
      "severity": "HIGH",
      "confidence": "MEDIUM",
      "cwe": {"id": "338", "url": "https://cwe.mitre.org/data/definitions/338.html"},
      "rule_id": "G404",
      "details": "Use of weak random number generator (math/rand or math/rand/v2 instead of crypto/rand)",
      "file": %q,
      "code": "11: \tz := 3\n12: \tfmt.Println(rand.Intn(10), x, y, z)\n13: }\n",
      "line": "12",
      "column": "14",
      "nosec": false,
      "suppressions": null
    }
  ]
}`

func TestGosecParsesJSON(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	src := "package main\n\nimport (\n\t\"fmt\"\n\t\"math/rand\"\n)\n\nfunc main() {\n\tx := 1\n\ty := 2\n\tz := 3\n\tfmt.Println(rand.Intn(10), x, y, z)\n}\n"
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	gosecJSON := fmt.Sprintf(gosecJSONTemplate, file)

	hits, err := NewGosec(fakeRunner{stdout: gosecJSON, exitCode: 1}, symbol.NewGo()).
		Run(context.Background(), dir, nil)
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
	if h.Line != 12 {
		t.Errorf("Line = %d, want 12", h.Line)
	}
	if h.Severity != finding.SeverityHigh {
		t.Errorf("Severity = %q, want high", h.Severity)
	}
	const want = `fmt.Println(rand.Intn(10), x, y, z)`
	if h.MatchText != want {
		t.Errorf("MatchText = %q, want %q (the single flagged source line)", h.MatchText, want)
	}
	for _, digit := range []string{"11", "12", "13"} {
		if strings.Contains(h.MatchText, digit+":") {
			t.Errorf("MatchText = %q retains a gosec context-window line prefix %q", h.MatchText, digit+":")
		}
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
