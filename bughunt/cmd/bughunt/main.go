// Command bughunt finds bugs, records them, and turns them into static checks.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/Fewbytes/sweatshop/bughunt/internal/cli"
	"github.com/Fewbytes/sweatshop/bughunt/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one command and returns the process exit code. Exit codes:
// 0 success, 1 findings that gate, 2 usage error, 3 internal error.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "bughunt %s (%s)\n", version.Version, version.Commit)
		return 0
	case "init":
		if err := cli.Init(".", stdout); err != nil {
			fmt.Fprintf(stderr, "init: %v\n", err)
			return 3
		}
		return 0
	case "scan":
		code, err := cli.Scan(context.Background(), ".", args[1:], stdout, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "scan: %v\n", err)
		}
		return code
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s\n", args[0], usage)
		return 2
	}
}

const usage = `usage: bughunt <command> [flags]

commands:
  init                 create .bughunt/ and report detector availability
  scan [--diff <rev>]  run deterministic detectors; exit 1 on gating findings
  version              print version and exit`
