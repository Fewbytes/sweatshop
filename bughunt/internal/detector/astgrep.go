package detector

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// NewAstGrep runs the repo-local rules via configPath (an sgconfig.yml file that
// specifies ruleDirs). This is the detector that emitted rules feed into, so it
// is the one that grows as the tool learns.
func NewAstGrep(run Runner, sym symbol.Resolver, rulesDir, configPath string) Detector {
	return &astGrep{run: run, sym: sym, rulesDir: rulesDir, configPath: configPath}
}

type astGrep struct {
	run        Runner
	sym        symbol.Resolver
	rulesDir   string
	configPath string
}

func (a *astGrep) Name() string { return "astgrep" }

// Available requires three things: the ast-grep binary, at least one .yaml file
// in rulesDir, and the config file to exist. Reporting the engine as available
// when any of these is missing would record a detector that cannot possibly find anything.
func (a *astGrep) Available(context.Context) bool {
	if !lookPath("ast-grep") {
		return false
	}
	matches, err := filepath.Glob(filepath.Join(a.rulesDir, "*.yaml"))
	if err != nil || len(matches) == 0 {
		return false
	}
	_, err = os.Stat(a.configPath)
	return err == nil
}

type astGrepMatch struct {
	RuleID   string `json:"ruleId"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	File     string `json:"file"`
	Text     string `json:"text"`
	Range    struct {
		Start struct {
			Line int `json:"line"`
		} `json:"start"`
	} `json:"range"`
}

func (a *astGrep) Run(ctx context.Context, dir string, paths []string) ([]finding.Hit, error) {
	args := []string{"scan", "--json", "-c", a.configPath}
	args = append(args, paths...)
	stdout, _, _, rawRef, err := a.run.Run(ctx, "ast-grep", args, dir)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(stdout) == "" {
		return nil, nil
	}

	var matches []astGrepMatch
	if err := json.Unmarshal([]byte(stdout), &matches); err != nil {
		return nil, err
	}

	var hits []finding.Hit
	for _, m := range matches {
		line := m.Range.Start.Line + 1 // ast-grep lines are 0-indexed
		abs := m.File
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(dir, m.File)
		}
		hits = append(hits, finding.Hit{
			RuleID:    "astgrep/" + m.RuleID,
			File:      m.File,
			Line:      line,
			Symbol:    symbol.Resolve(a.sym, abs, line),
			Message:   m.Message,
			Severity:  astGrepSeverity(m.Severity),
			MatchText: strings.TrimSpace(m.Text),
			RawRef:    rawRef,
		})
	}
	return hits, nil
}

func astGrepSeverity(s string) finding.Severity {
	switch strings.ToLower(s) {
	case "error":
		return finding.SeverityHigh
	case "warning":
		return finding.SeverityMedium
	default:
		return finding.SeverityLow
	}
}
