// Command depmaint expands, caches, and drives dependency-maintenance campaigns.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/Fewbytes/sweatshop/depmaint/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one command and returns the process exit code.
// 0 success, 2 usage error, 3 internal error.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "depmaint %s (%s)\n", version.Version, version.Commit)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s\n", args[0], usage)
		return 2
	}
}

const usage = `usage: depmaint <command> [flags]

commands:
  init                  create .depmaint/ and default config
  graph [--refresh]     expand + snapshot; report drift vs previous
  version               print version and exit`
