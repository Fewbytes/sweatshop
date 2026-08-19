package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"github.com/Fewbytes/sweatshop/agentsh/internal/config"
	"github.com/Fewbytes/sweatshop/agentsh/internal/daemon"
	"github.com/Fewbytes/sweatshop/agentsh/internal/mcp"
	"github.com/Fewbytes/sweatshop/agentsh/internal/version"
	"github.com/Fewbytes/sweatshop/agentsh/internal/workspace"
)

// supportedGOOS is the declared platform matrix: Linux gets full containment
// (cgroups, see internal/executor/cgroup_linux.go); macOS/arm64 runs the
// degraded fallback path. Windows is untested and darwin/amd64 doesn't even
// build — github.com/tursodatabase/go-libsql only ships a prebuilt static
// lib for linux_amd64, linux_arm64, and darwin_arm64 (no darwin_amd64), so
// an Intel Mac binary can't be produced at all. Rather than silently run a
// broken (or nonexistent) daemon, refuse to start with a clear message.
var supportedGOOS = map[string]bool{"linux": true, "darwin": true}

func platformSupported(goos, goarch string) bool {
	if !supportedGOOS[goos] {
		return false
	}
	if goos == "darwin" && goarch != "arm64" {
		return false
	}
	return true
}

func main() {
	workspaceFlag := flag.String("workspace", "", "workspace directory")
	configFlag := flag.String("config", "", "config file (default: <workspace>/.agentsh/config.json, then ~/.config/agentsh/config.json)")
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Println("agentshd", version.String())
		return
	}
	if !platformSupported(runtime.GOOS, runtime.GOARCH) {
		fmt.Fprintf(os.Stderr, "agentshd: unsupported platform %s/%s (supported: linux/amd64, linux/arm64, darwin/arm64) — see sweatshop-u7z\n", runtime.GOOS, runtime.GOARCH)
		os.Exit(1)
	}

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
