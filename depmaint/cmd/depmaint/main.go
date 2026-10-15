// Command depmaint expands, caches, and drives dependency-maintenance campaigns.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Fewbytes/sweatshop/depmaint/internal/cli"
	"github.com/Fewbytes/sweatshop/depmaint/internal/version"

	_ "github.com/Fewbytes/sweatshop/depmaint/internal/goexpander"
	_ "github.com/Fewbytes/sweatshop/depmaint/internal/jvmexpander"
	_ "github.com/Fewbytes/sweatshop/depmaint/internal/nodeexpander"
	_ "github.com/Fewbytes/sweatshop/depmaint/internal/pyexpander"
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
	case "init":
		if err := cli.Init(".", stdout); err != nil {
			fmt.Fprintf(stderr, "init: %v\n", err)
			return 3
		}
		return 0
	case "graph":
		fs := flag.NewFlagSet("graph", flag.ContinueOnError)
		refresh := fs.Bool("refresh", false, "re-expand even if a cached snapshot exists")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if err := cli.Graph(context.Background(), ".", *refresh, stdout); err != nil {
			fmt.Fprintf(stderr, "graph: %v\n", err)
			return 3
		}
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
