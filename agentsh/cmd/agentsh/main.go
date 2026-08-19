package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	agentrpc "github.com/Fewbytes/sweatshop/agentsh/internal/rpc"
	"github.com/Fewbytes/sweatshop/agentsh/internal/workspace"
)

func main() {
	workspaceFlag := flag.String("workspace", "", "workspace directory")
	flag.Parse()
	if flag.NArg() < 1 {
		fatal("usage: agentsh [--workspace DIR] health|shutdown|bash COMMAND|output ID|processes [--session NAME]")
	}

	paths, err := workspace.Resolve(*workspaceFlag)
	if err != nil {
		fatal(err.Error())
	}
	// The dial timeout stays short, but a command may run to the daemon's own
	// limit; the overall bound must sit above it so the CLI does not abandon a
	// result the daemon is still producing.
	client := agentrpc.Client{Socket: paths.Socket, Timeout: 180 * time.Second}
	op := flag.Arg(0)
	args := flag.Args()[1:]
	if op == "output" {
		op = agentrpc.OpBashOutput
	}
	if op == "processes" {
		op = agentrpc.OpBashProcesses
	}
	if op != agentrpc.OpHealth && op != agentrpc.OpShutdown && op != agentrpc.OpBash && op != agentrpc.OpBashOutput && op != agentrpc.OpBashProcesses {
		fatal("unknown command: " + op)
	}
	if (op == agentrpc.OpBash || op == agentrpc.OpBashOutput) && len(args) != 1 {
		fatal("bash and output commands require one argument")
	}
	if op == agentrpc.OpBashProcesses && len(args) > 2 {
		fatal("usage: agentsh processes [--session NAME]")
	}
	if err := call(client, paths, op, args); err != nil {
		fatal(err.Error())
	}
}

func call(client agentrpc.Client, paths workspace.Paths, op string, args []string) error {
	request := agentrpc.Request{ID: requestID(), Op: op}
	var result any
	switch op {
	case agentrpc.OpHealth:
		result = &agentrpc.Health{}
	case agentrpc.OpBash:
		request.Params, _ = json.Marshal(agentrpc.BashRequest{Command: args[0]})
		result = &map[string]any{}
	case agentrpc.OpBashOutput:
		request.Params, _ = json.Marshal(agentrpc.BashOutputRequest{ID: args[0]})
		result = &map[string]any{}
	case agentrpc.OpBashProcesses:
		var session string
		if len(args) == 2 && args[0] == "--session" {
			session = args[1]
		} else if len(args) != 0 {
			return fmt.Errorf("usage: agentsh processes [--session NAME]")
		}
		request.Params, _ = json.Marshal(agentrpc.BashStateRequest{Session: session})
		result = &[]map[string]any{}
	default:
		result = &map[string]string{}
	}
	err := client.Call(context.Background(), request, result)
	if err != nil && op != agentrpc.OpShutdown && unavailable(err) {
		if err = startDaemon(paths); err != nil {
			return err
		}
		err = client.Call(context.Background(), request, result)
	}
	if err != nil {
		return err
	}
	encoded, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(encoded))
	return nil
}

func startDaemon(paths workspace.Paths) error {
	if err := paths.Ensure(); err != nil {
		return err
	}
	binary, err := daemonBinary()
	if err != nil {
		return err
	}
	log, err := os.OpenFile(paths.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(binary, "--workspace", paths.Root)
	cmd.Stdin = nil
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = detachedProcessAttributes()
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", paths.Socket, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not start; inspect %s", paths.Log)
}

func daemonBinary() (string, error) {
	if override := os.Getenv("AGENTSHD_PATH"); override != "" {
		return override, nil
	}
	self, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(self), "agentshd")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, nil
		}
	}
	return exec.LookPath("agentshd")
}

func unavailable(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

func requestID() string {
	var value [8]byte
	_, _ = rand.Read(value[:])
	return hex.EncodeToString(value[:])
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, "agentsh:", message)
	os.Exit(1)
}
