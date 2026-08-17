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

	agentrpc "github.com/avishai-ish-shalom/sweatshop/agentsh/internal/rpc"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/workspace"
)

func main() {
	workspaceFlag := flag.String("workspace", "", "workspace directory")
	flag.Parse()
	if flag.NArg() < 1 {
		fatal("usage: agentsh [--workspace DIR] health|shutdown|bash COMMAND|output ID")
	}

	paths, err := workspace.Resolve(*workspaceFlag)
	if err != nil {
		fatal(err.Error())
	}
	client := agentrpc.Client{Socket: paths.Socket}
	op := flag.Arg(0)
	if op == "output" {
		op = agentrpc.OpBashOutput
	}
	if op != agentrpc.OpHealth && op != agentrpc.OpShutdown && op != agentrpc.OpBash && op != agentrpc.OpBashOutput {
		fatal("unknown command: " + op)
	}
	if (op == agentrpc.OpBash || op == agentrpc.OpBashOutput) && flag.NArg() != 2 {
		fatal("bash and output commands require one argument")
	}
	if err := call(client, paths, op, flag.Args()[1:]); err != nil {
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
