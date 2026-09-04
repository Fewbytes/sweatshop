package detector

import (
	"bufio"
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// diagnosticLine matches the "file:line:col: message" form every Go analysis
// tool in this package emits.
var diagnosticLine = regexp.MustCompile(`^(.+?):(\d+):(\d+): (.+)$`)

// NewGoVet adapts `go vet`, which writes diagnostics to stderr.
func NewGoVet(run Runner, sym symbol.Resolver) Detector {
	return &goVet{run: run, sym: sym}
}

type goVet struct {
	run Runner
	sym symbol.Resolver
}

func (g *goVet) Name() string { return "govet" }

func (g *goVet) Available(context.Context) bool { return lookPath("go") }

func (g *goVet) Run(ctx context.Context, dir string, paths []string) ([]finding.Hit, error) {
	args := []string{"vet"}
	if len(paths) == 0 {
		args = append(args, "./...")
	} else {
		args = append(args, paths...)
	}
	_, stderr, _, rawRef, err := g.run.Run(ctx, "go", args, dir)
	if err != nil {
		return nil, err
	}
	return parseDiagnostics(stderr, rawRef, g.sym, vetRuleID, finding.SeverityMedium), nil
}

// vetRuleID derives a rule id from the analyzer that produced the message. go
// vet does not label its analyzers, so the first recognizable token is used;
// unrecognized messages fall back to a generic id.
func vetRuleID(message string) string {
	analyzers := []struct{ prefix, name string }{
		{"fmt.Printf", "printf"}, {"fmt.Sprintf", "printf"}, {"fmt.Errorf", "printf"},
		{"lost cancel", "lostcancel"},
		{"unreachable", "unreachable"},
		{"self-assignment", "assign"},
		{"suspect or", "bools"},
		{"struct field", "structtag"},
		{"the cancel function", "lostcancel"},
	}
	for _, a := range analyzers {
		if strings.Contains(message, a.prefix) {
			return "govet/" + a.name
		}
	}
	return "govet/vet"
}

// parseDiagnostics turns "file:line:col: message" output into hits, resolving
// the enclosing symbol and reading back the flagged source line as match text.
func parseDiagnostics(output, rawRef string, sym symbol.Resolver,
	ruleID func(string) string, severity finding.Severity) []finding.Hit {

	var hits []finding.Hit
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		m := diagnosticLine.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		file := m[1]
		line, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		message := m[4]
		hits = append(hits, finding.Hit{
			RuleID:    ruleID(message),
			File:      file,
			Line:      line,
			Symbol:    symbol.Resolve(sym, file, line),
			Message:   trimCheckSuffix(message),
			Severity:  severity,
			MatchText: sourceLine(file, line),
			RawRef:    rawRef,
		})
	}
	return hits
}

// sourceLine returns the trimmed contents of a 1-indexed line, or "" if the
// file cannot be read. An unreadable file degrades identity to rule+symbol,
// which is still stable, so this is not an error.
func sourceLine(path string, line int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	if line < 1 || line > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line-1])
}
