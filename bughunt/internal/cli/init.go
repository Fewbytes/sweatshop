package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/Fewbytes/sweatshop/bughunt/internal/store"
)

const defaultConfig = `# bughunt configuration. Every field is optional.
#
# detectors:      set a detector to false to disable it; unlisted means enabled
# severity_floor: low | medium | high — drops hits below this rank
# exclude:        glob patterns matched against repo-relative paths

detectors: {}
severity_floor: low
exclude:
  - vendor/**
`

// defaultSgConfig is the ast-grep project config bughunt writes on init.
// ruleDirs entries resolve relative to this file's own directory, and
// .bughunt/rules is where bughunt keeps rules, so "rules" is correct here.
const defaultSgConfig = `ruleDirs:
  - rules
`

// Init creates the .bughunt directory, database, and default config, then
// reports which detectors are installed. Re-running it never overwrites an
// existing config or sgconfig.
func Init(root string, out io.Writer) error {
	l := Paths(root)
	for _, dir := range []string{l.Dir, l.Rules, l.Lessons} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	if err := writeIfAbsent(l.Config, defaultConfig, out); err != nil {
		return err
	}
	if err := writeIfAbsent(l.SgConfig, defaultSgConfig, out); err != nil {
		return err
	}

	s, err := store.Open(l.DB)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Migrate(context.Background()); err != nil {
		return err
	}
	fmt.Fprintf(out, "database ready at %s\n", l.DB)

	reportDetectors(root, out)
	return nil
}

// writeIfAbsent writes content to path only if nothing is there yet, so a
// re-run never clobbers a user's edits. It reports which happened, in the
// same style for every file init owns.
func writeIfAbsent(path, content string, out io.Writer) error {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "wrote %s\n", path)
		return nil
	} else if err != nil {
		return err
	}
	fmt.Fprintf(out, "kept existing %s\n", path)
	return nil
}

// reportDetectors prints installed and missing tools with install hints, so a
// first run tells the user exactly what is degraded and how to fix it.
func reportDetectors(root string, out io.Writer) {
	ctx := context.Background()
	reg := Registry(root)
	available, unavailable := reg.Partition(ctx, reg.All())

	fmt.Fprintln(out, "\ndetectors available:")
	for _, d := range available {
		fmt.Fprintf(out, "  %s\n", d.Name())
	}
	if len(unavailable) == 0 {
		return
	}
	fmt.Fprintln(out, "\ndetectors missing (scans will run without them):")
	for _, d := range unavailable {
		fmt.Fprintf(out, "  %-12s %s\n", d.Name(), installHint[d.Name()])
	}
}

var installHint = map[string]string{
	"govet":       "install the Go toolchain",
	"staticcheck": "go install honnef.co/go/tools/cmd/staticcheck@latest",
	"errcheck":    "go install github.com/kisielk/errcheck@latest",
	"ineffassign": "go install github.com/gordonklaus/ineffassign@latest",
	"gosec":       "go install github.com/securego/gosec/v2/cmd/gosec@latest",
	"astgrep":     "install ast-grep, then add rules under .bughunt/rules/",
}
