package finding

import "testing"

func base() Hit {
	return Hit{RuleID: "govet/printf", File: "internal/scan/scan.go", Line: 42,
		Symbol: "Run", MatchText: "fmt.Printf(\"%d\", s)", Severity: SeverityMedium}
}

func TestFingerprintIsStableAcrossLineMoves(t *testing.T) {
	a := base()
	b := base()
	b.Line = 998
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("line number must not affect the fingerprint")
	}
}

func TestFingerprintIsStableAcrossReformatting(t *testing.T) {
	a := base()
	b := base()
	b.MatchText = "fmt.Printf(\"%d\",\n\t\ts)"
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("whitespace-only differences must not affect the fingerprint")
	}
}

func TestFingerprintIsStableAcrossFileMoves(t *testing.T) {
	a := base()
	b := base()
	b.File = "internal/scanner/scan.go"
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("a symbol moved to another file keeps its fingerprint")
	}
}

func TestFingerprintChangesWhenSymbolRenamed(t *testing.T) {
	a := base()
	b := base()
	b.Symbol = "Execute"
	if Fingerprint(a) == Fingerprint(b) {
		t.Fatal("renaming the enclosing symbol must produce a new fingerprint")
	}
}

func TestFingerprintChangesWithRuleAndMatch(t *testing.T) {
	a := base()
	byRule := base()
	byRule.RuleID = "gosec/G404"
	if Fingerprint(a) == Fingerprint(byRule) {
		t.Fatal("different rules must not collide")
	}
	byMatch := base()
	byMatch.MatchText = "fmt.Printf(\"%s\", s)"
	if Fingerprint(a) == Fingerprint(byMatch) {
		t.Fatal("different matched code must not collide")
	}
}

func TestFingerprintFallsBackToFileWithoutSymbol(t *testing.T) {
	a := base()
	a.Symbol = ""
	b := a
	b.File = "other.go"
	if Fingerprint(a) == Fingerprint(b) {
		t.Fatal("without a symbol, the file path anchors the fingerprint")
	}
}

func TestFingerprintLength(t *testing.T) {
	if got := len(Fingerprint(base())); got != 16 {
		t.Fatalf("length = %d, want 16", got)
	}
}

func TestNormalize(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"  a   b \n\t c ", "a b c"},
		{"a\n\nb", "a b"},
		{"", ""},
	} {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSeverityAtLeast(t *testing.T) {
	if !SeverityHigh.AtLeast(SeverityLow) {
		t.Error("high >= low")
	}
	if SeverityLow.AtLeast(SeverityMedium) {
		t.Error("low < medium")
	}
	if !SeverityMedium.AtLeast(SeverityMedium) {
		t.Error("medium >= medium")
	}
}
