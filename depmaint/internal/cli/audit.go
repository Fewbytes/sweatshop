package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/Fewbytes/sweatshop/depmaint/internal/config"
	"github.com/Fewbytes/sweatshop/depmaint/internal/enrich"
)

// auditFinding is one worklist row: a node with a reason to act on it.
type auditFinding struct {
	Ref      nodeRef
	Severity enrich.Severity // advisories rank by CVSS; deprecated and EOL rank high
	Reason   string
	Action   string // suggested campaign type
}

// Audit joins enrichment against root's latest snapshot — advisories,
// deprecation, and runtime EOL — and prints a worklist of suggested
// campaigns, most severe first. minSeverity ("" = config's severity_floor)
// drops advisories ranked below it. Packages named in config's ignore list,
// or whose ecosystem is, are skipped. A lookup that fails is a warning, not
// a failure.
func Audit(ctx context.Context, root, minSeverity string, pub Published, out io.Writer) error {
	l := Paths(root)
	cfg, err := config.Load(l.Dir)
	if err != nil {
		return err
	}
	if minSeverity == "" {
		minSeverity = cfg.SeverityFloor
	}
	floor, err := enrich.ParseSeverity(minSeverity)
	if err != nil {
		return err
	}
	s, nodes, err := openCache(ctx, root)
	if err != nil {
		return err
	}
	defer s.Close()

	var findings []auditFinding
	for _, n := range nodes {
		ref, err := parseNodeKey(n.Key)
		if err != nil {
			return err
		}
		if slices.Contains(cfg.Ignore, ref.Name) || slices.Contains(cfg.Ignore, ref.Ecosystem) || strings.HasPrefix(ref.Version, "(") {
			continue
		}
		var got []auditFinding
		if n.Kind == "runtime" {
			got, err = auditRuntime(ctx, s, pub, out, ref)
		} else if system, ok := enrich.System(ref.Ecosystem); ok {
			got, err = auditPackage(ctx, s, pub, out, ref, system, floor)
		}
		if err != nil {
			fmt.Fprintf(out, "warning: %s: %v\n", ref.Key, err)
			continue
		}
		findings = append(findings, got...)
	}

	slices.SortFunc(findings, func(a, b auditFinding) int {
		if a.Severity != b.Severity {
			return int(b.Severity) - int(a.Severity)
		}
		return strings.Compare(a.Ref.Key+a.Reason, b.Ref.Key+b.Reason)
	})
	if len(findings) == 0 {
		fmt.Fprintln(out, "audit clean: no advisories, deprecations, or EOL runtimes")
		return nil
	}
	for _, f := range findings {
		fmt.Fprintf(out, "[%s] %s — %s → suggest: %s campaign\n", f.Severity, f.Ref.Key, f.Reason, f.Action)
	}
	return nil
}

func auditPackage(ctx context.Context, s enrich.Store, pub Published, out io.Writer, ref nodeRef, system string, floor enrich.Severity) ([]auditFinding, error) {
	var detail enrich.VersionDetail
	err := fetchDecoded(ctx, s, pub.Now, out, enrich.SourceVersion, enrich.VersionKey(system, ref.Name, ref.Version),
		func(ctx context.Context) ([]byte, error) {
			return pub.DepsDev.FetchVersion(ctx, system, ref.Name, ref.Version)
		}, &detail)
	if err != nil {
		return nil, err
	}
	var found []auditFinding
	if detail.IsDeprecated {
		found = append(found, auditFinding{ref, enrich.SeverityHigh, "deprecated", "upgrade or remove"})
	}
	for _, k := range detail.AdvisoryKeys {
		var adv enrich.Advisory
		err := fetchDecoded(ctx, s, pub.Now, out, enrich.SourceAdvisories, k.ID,
			func(ctx context.Context) ([]byte, error) { return pub.DepsDev.FetchAdvisory(ctx, k.ID) }, &adv)
		if err != nil {
			return nil, err
		}
		if adv.Severity() < floor {
			continue
		}
		found = append(found, auditFinding{ref, adv.Severity(), fmt.Sprintf("advisory %s: %s", k.ID, adv.Title), "upgrade"})
	}
	return found, nil
}

// auditRuntime checks a runtime node, keyed "runtime:<endoflife.date
// product>@<version>", against endoflife.date.
func auditRuntime(ctx context.Context, s enrich.Store, pub Published, out io.Writer, ref nodeRef) ([]auditFinding, error) {
	c, err := enrich.Fetch(ctx, s, pub.Now, enrich.SourceEOL, ref.Name,
		func(ctx context.Context) ([]byte, error) { return pub.EndOfLife.FetchProduct(ctx, ref.Name) })
	if err != nil {
		return nil, err
	}
	noteStale(out, enrich.SourceEOL, ref.Name, c)
	cycles, err := enrich.ParseCycles(c.Payload)
	if err != nil {
		return nil, err
	}
	cycle, ok := enrich.CycleFor(cycles, ref.Version)
	if !ok || !cycle.IsEOL(pub.Now()) {
		return nil, nil
	}
	return []auditFinding{{ref, enrich.SeverityHigh, "end of life (cycle " + cycle.Name + ")", "runtime"}}, nil
}
