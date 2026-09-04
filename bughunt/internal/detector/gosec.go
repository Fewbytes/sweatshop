package detector

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// NewGosec adapts gosec, which emits JSON and reports its own severities.
func NewGosec(run Runner, sym symbol.Resolver) Detector {
	return &gosec{run: run, sym: sym}
}

type gosec struct {
	run Runner
	sym symbol.Resolver
}

func (g *gosec) Name() string { return "gosec" }

func (g *gosec) Available(context.Context) bool { return lookPath("gosec") }

type gosecReport struct {
	Issues []struct {
		Severity string `json:"severity"`
		RuleID   string `json:"rule_id"`
		Details  string `json:"details"`
		File     string `json:"file"`
		Line     string `json:"line"`
		Code     string `json:"code"`
	} `json:"Issues"`
}

func (g *gosec) Run(ctx context.Context, dir string, paths []string) ([]finding.Hit, error) {
	stdout, _, _, rawRef, err := g.run.Run(ctx, "gosec",
		append([]string{"-fmt=json", "-quiet"}, targets(paths)...), dir)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(stdout) == "" {
		return nil, nil
	}

	var report gosecReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		return nil, err
	}

	var hits []finding.Hit
	for _, issue := range report.Issues {
		// gosec reports line as a string, sometimes as a "12-14" range.
		lineText, _, _ := strings.Cut(issue.Line, "-")
		line, err := strconv.Atoi(lineText)
		if err != nil {
			continue
		}
		hits = append(hits, finding.Hit{
			RuleID:    "gosec/" + issue.RuleID,
			File:      issue.File,
			Line:      line,
			Symbol:    symbol.Resolve(g.sym, issue.File, line),
			Message:   issue.Details,
			Severity:  gosecSeverity(issue.Severity),
			MatchText: strings.TrimSpace(issue.Code),
			RawRef:    rawRef,
		})
	}
	return hits, nil
}

func gosecSeverity(s string) finding.Severity {
	switch strings.ToUpper(s) {
	case "HIGH":
		return finding.SeverityHigh
	case "MEDIUM":
		return finding.SeverityMedium
	default:
		return finding.SeverityLow
	}
}
