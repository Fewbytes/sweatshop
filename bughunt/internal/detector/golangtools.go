package detector

import (
	"context"
	"regexp"
	"strings"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// lineTool adapts any tool emitting "file:line:col: message" on stdout.
type lineTool struct {
	name     string
	binary   string
	args     func(paths []string) []string
	ruleID   func(message string) string
	severity finding.Severity
	run      Runner
	sym      symbol.Resolver
}

func (t *lineTool) Name() string { return t.name }

func (t *lineTool) Available(context.Context) bool { return lookPath(t.binary) }

func (t *lineTool) Run(ctx context.Context, dir string, paths []string) ([]finding.Hit, error) {
	stdout, _, _, rawRef, err := t.run.Run(ctx, t.binary, t.args(paths), dir)
	if err != nil {
		return nil, err
	}
	return parseDiagnostics(stdout, rawRef, t.sym, t.ruleID, t.severity), nil
}

func targets(paths []string) []string {
	if len(paths) == 0 {
		return []string{"./..."}
	}
	return paths
}

// staticcheckSuffix captures the check id staticcheck appends, e.g. "(SA4006)".
// The U family (unused code) is included: without it those findings collapse to
// the generic id, which defeats per-rule accounting for one of the most common
// checks staticcheck reports.
var staticcheckSuffix = regexp.MustCompile(`\s*\((S[A-Z]?\d{4}|ST\d{4}|QF\d{4}|U\d{4})\)$`)

// NewStaticcheck adapts staticcheck.
func NewStaticcheck(run Runner, sym symbol.Resolver) Detector {
	return &lineTool{
		name: "staticcheck", binary: "staticcheck",
		args:     targets,
		ruleID:   staticcheckRuleID,
		severity: finding.SeverityMedium,
		run:      run, sym: sym,
	}
}

func staticcheckRuleID(message string) string {
	if m := staticcheckSuffix.FindStringSubmatch(message); m != nil {
		return "staticcheck/" + m[1]
	}
	return "staticcheck/staticcheck"
}

// NewErrcheck adapts errcheck. Unchecked errors are one class, so the rule id
// is fixed.
func NewErrcheck(run Runner, sym symbol.Resolver) Detector {
	return &lineTool{
		name: "errcheck", binary: "errcheck",
		args:     targets,
		ruleID:   func(string) string { return "errcheck/unchecked" },
		severity: finding.SeverityMedium,
		run:      run, sym: sym,
	}
}

// NewIneffassign adapts ineffassign.
func NewIneffassign(run Runner, sym symbol.Resolver) Detector {
	return &lineTool{
		name: "ineffassign", binary: "ineffassign",
		args:     targets,
		ruleID:   func(string) string { return "ineffassign/ineffassign" },
		severity: finding.SeverityLow,
		run:      run, sym: sym,
	}
}

// trimCheckSuffix strips a trailing check id from a message so the same defect
// reported with and without a suffix reads identically.
func trimCheckSuffix(message string) string {
	return strings.TrimSpace(staticcheckSuffix.ReplaceAllString(message, ""))
}
