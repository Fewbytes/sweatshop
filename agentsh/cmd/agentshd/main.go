package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/daemon"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/mcp"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/workspace"
)

func main() {
	workspaceFlag := flag.String("workspace", "", "workspace directory")
	flag.Parse()

	paths, err := workspace.Resolve(*workspaceFlag)
	if err == nil {
		if flag.NArg() > 0 && flag.Arg(0) == "mcp" {
			// MCP transport and workspace daemon share one process. Start the
			// stateful daemon behind the transport before accepting tool calls.
			daemonErr := make(chan error, 1)
			go func() { daemonErr <- daemon.New(paths).Serve(context.Background()) }()
			for i := 0; i < 100; i++ {
				if _, statErr := os.Stat(paths.Socket); statErr == nil {
					break
				}
				select {
				case err = <-daemonErr:
					break
				default:
				}
				if err != nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err == nil {
				err = mcp.New(paths).Serve(context.Background(), os.Stdin, os.Stdout)
			}
		} else {
			err = daemon.New(paths).Serve(context.Background())
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentshd:", err)
		os.Exit(1)
	}
}
