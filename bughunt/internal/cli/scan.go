package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/Fewbytes/sweatshop/bughunt/internal/config"
	"github.com/Fewbytes/sweatshop/bughunt/internal/detector"
	"github.com/Fewbytes/sweatshop/bughunt/internal/gitinfo"
	"github.com/Fewbytes/sweatshop/bughunt/internal/scan"
	"github.com/Fewbytes/sweatshop/bughunt/internal/store"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// Registry builds the detector set for a repository. Every detector shares one
// symbol resolver so each file is parsed once per scan.
func Registry(root string) *detector.Registry {
	run := detector.NewExecRunner()
	sym := symbol.NewGo()
	return detector.NewRegistry(
		detector.NewGoVet(run, sym),
		detector.NewStaticcheck(run, sym),
		detector.NewErrcheck(run, sym),
		detector.NewIneffassign(run, sym),
		detector.NewGosec(run, sym),
		detector.NewAstGrep(run, sym, Paths(root).Rules, Paths(root).SgConfig),
	)
}

// Scan runs the deterministic detectors and returns the process exit code.
func Scan(ctx context.Context, root string, args []string, out, errOut io.Writer) (int, error) {
	// Named flags rather than fs, since io/fs is imported here.
	flags := flag.NewFlagSet("scan", flag.ContinueOnError)
	flags.SetOutput(errOut)
	diffBase := flags.String("diff", "", "gate only on findings touching lines changed since this revision")
	if err := flags.Parse(args); err != nil {
		return 2, err
	}

	l := Paths(root)
	if _, err := os.Stat(l.Dir); errors.Is(err, fs.ErrNotExist) {
		return 2, fmt.Errorf("no %s directory — run `bughunt init` first", Dir)
	} else if err != nil {
		return 3, err
	}

	cfg, err := config.Load(l.Dir)
	if err != nil {
		return 3, err
	}

	s, err := store.Open(l.DB)
	if err != nil {
		return 3, err
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		return 3, err
	}

	opts := scan.Options{Dir: root, DiffBase: *diffBase, Paths: flags.Args()}
	// Git facts are best-effort: bughunt must work in a directory that is not
	// a repository, it just cannot offer diff mode there.
	if sha, err := gitinfo.HeadSHA(root); err == nil {
		opts.CommitSHA = sha
	}
	if *diffBase != "" {
		changed, err := gitinfo.ChangedLines(root, *diffBase)
		if err != nil {
			return 2, fmt.Errorf("git diff against %q: %w", *diffBase, err)
		}
		opts.ChangedLines = changed
	}

	res, err := scan.Run(ctx, s, Registry(root), cfg, opts)
	if err != nil {
		return 3, err
	}
	printResult(out, res)
	return res.ExitCode(), nil
}

func printResult(out io.Writer, res scan.Result) {
	for _, f := range res.Findings {
		marker := " "
		if f.Gates {
			marker = "!"
		}
		fmt.Fprintf(out, "%s %s:%d [%s] %s (%s, %s)\n",
			marker, f.Hit.File, f.Hit.Line, f.Hit.RuleID, f.Hit.Message,
			f.Status, f.Fingerprint)
	}
	if len(res.DetectorsUnavailable) > 0 {
		fmt.Fprintf(out, "\nskipped unavailable detectors: %v\n", res.DetectorsUnavailable)
	}
	fmt.Fprintf(out, "\n%d findings, %d gating (run %s)\n",
		len(res.Findings), res.GatingCount, res.RunID)
}
