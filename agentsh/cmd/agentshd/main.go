package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/config"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/daemon"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/mcp"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/workspace"
)

func main() {
	workspaceFlag := flag.String("workspace", "", "workspace directory")
	configFlag := flag.String("config", "", "config file (default: <workspace>/.agentsh/config.json, then ~/.config/agentsh/config.json)")
	flag.Parse()

	var settings config.Config
	paths, err := workspace.Resolve(*workspaceFlag)
	if err == nil {
		settings, err = config.Load(*configFlag, paths.Root)
	}
	if err == nil {
		if flag.NArg() > 0 && flag.Arg(0) == "mcp" {
			// MCP transport and workspace daemon share one process, but only
			// when this workspace has no daemon yet. Every MCP client launch
			// starting its own daemon puts several of them on one database.
			if !daemonReachable(paths.Socket) {
				err = startDaemon(paths, settings)
			}
			if err == nil {
				err = mcp.New(paths).Serve(context.Background(), os.Stdin, os.Stdout)
			}
		} else {
			err = daemon.NewWithConfig(paths, settings).Serve(context.Background())
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentshd:", err)
		os.Exit(1)
	}
}

// daemonReachable reports whether a daemon is already serving this workspace.
// A socket file alone is not proof: a crashed daemon leaves one behind.
func daemonReachable(socket string) bool {
	conn, err := net.DialTimeout("unix", socket, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// startDaemon runs the workspace daemon behind the transport and waits for it
// to accept connections, so the first tool call cannot race the listener.
func startDaemon(paths workspace.Paths, settings config.Config) error {
	failed := make(chan error, 1)
	go func() { failed <- daemon.NewWithConfig(paths, settings).Serve(context.Background()) }()
	for i := 0; i < 100; i++ {
		select {
		case err := <-failed:
			if err == nil {
				return errors.New("daemon stopped before it began serving")
			}
			return err
		default:
		}
		if daemonReachable(paths.Socket) {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not start serving %s within 1s", paths.Socket)
}
