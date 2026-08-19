package main

import (
	"bytes"
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
	"sort"
	"syscall"
	"time"

	agentrpc "github.com/Fewbytes/sweatshop/agentsh/internal/rpc"
	"github.com/Fewbytes/sweatshop/agentsh/internal/workspace"
)

// command builds an RPC request from a subcommand's own flag.FlagSet, so
// each op's flags are declared once, next to the op, instead of a shared
// hand-rolled arg scanner having to know about every op's fields.
type command struct {
	usage string
	build func(args []string) (agentrpc.Request, error)
}

var commands = map[string]command{
	"health":         {"agentsh health", buildHealth},
	"shutdown":       {"agentsh shutdown", buildShutdown},
	"bash":           {"agentsh bash [flags] COMMAND", buildBash},
	"output":         {"agentsh output [flags] ID", buildOutput},
	"input":          {"agentsh input ID DATA", buildInput},
	"kill":           {"agentsh kill [flags] ID", buildKill},
	"state":          {"agentsh state [flags]", buildState},
	"history":        {"agentsh history [flags]", buildHistory},
	"replay":         {"agentsh replay ID", buildReplay},
	"processes":      {"agentsh processes [flags]", buildProcesses},
	"gc":             {"agentsh gc [flags]", buildGC},
	"templates":      {"agentsh templates [flags] ID", buildTemplates},
	"service":        {"agentsh service [flags] NAME COMMAND", buildService},
	"service-status": {"agentsh service-status NAME", buildServiceStatus},
	"service-kill":   {"agentsh service-kill [flags] NAME", buildServiceKill},
	"service-logs":   {"agentsh service-logs [flags] NAME", buildServiceLogs},
}

func main() {
	workspaceFlag := flag.String("workspace", "", "workspace directory")
	flag.Usage = printUsage
	flag.Parse()
	if flag.NArg() < 1 {
		printUsage()
		os.Exit(1)
	}

	name := flag.Arg(0)
	cmd, ok := commands[name]
	if !ok {
		fatal("unknown command: " + name)
	}

	request, err := cmd.build(flag.Args()[1:])
	if err != nil {
		fatal(err.Error())
	}
	request.ID = requestID()

	paths, err := workspace.Resolve(*workspaceFlag)
	if err != nil {
		fatal(err.Error())
	}
	// The dial timeout stays short, but a command may run to the daemon's own
	// limit; the overall bound must sit above it so the CLI does not abandon a
	// result the daemon is still producing.
	client := agentrpc.Client{Socket: paths.Socket, Timeout: 180 * time.Second}
	if err := call(client, paths, request); err != nil {
		fatal(err.Error())
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: agentsh [--workspace DIR] <command> [flags] [args]")
	fmt.Fprintln(os.Stderr, "\ncommands:")
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(os.Stderr, "  %s\n", commands[name].usage)
	}
	fmt.Fprintln(os.Stderr, "\nrun `agentsh <command> -h` for a command's flags")
}

func call(client agentrpc.Client, paths workspace.Paths, request agentrpc.Request) error {
	var result json.RawMessage
	err := client.Call(context.Background(), request, &result)
	if err != nil && request.Op != agentrpc.OpShutdown && unavailable(err) {
		if err = startDaemon(paths); err != nil {
			return err
		}
		err = client.Call(context.Background(), request, &result)
	}
	if err != nil {
		return err
	}
	if len(result) == 0 {
		return nil
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, result, "", "  "); err != nil {
		// Not JSON-shaped for some reason; print the raw bytes rather than
		// hide a response the daemon did send.
		fmt.Println(string(result))
		return nil
	}
	fmt.Println(indented.String())
	return nil
}

// --- per-command flag parsing -------------------------------------------

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	return fs
}

func buildHealth(args []string) (agentrpc.Request, error) {
	fs := newFlagSet("health")
	if err := fs.Parse(args); err != nil {
		return agentrpc.Request{}, err
	}
	return agentrpc.Request{Op: agentrpc.OpHealth}, nil
}

func buildShutdown(args []string) (agentrpc.Request, error) {
	fs := newFlagSet("shutdown")
	if err := fs.Parse(args); err != nil {
		return agentrpc.Request{}, err
	}
	return agentrpc.Request{Op: agentrpc.OpShutdown}, nil
}

func buildBash(args []string) (agentrpc.Request, error) {
	fs := newFlagSet("bash")
	session := fs.String("session", "", "session name")
	timeout := fs.Duration("timeout", 0, "command timeout")
	idleWait := fs.Duration("idle-wait", 0, "idle-input detection timeout")
	background := fs.Bool("background", false, "run in the background, return immediately")
	interactive := fs.Bool("interactive", false, "keep stdin open for BashInput")
	stdin := fs.String("stdin", "", "text to write to the command's stdin")
	if err := fs.Parse(args); err != nil {
		return agentrpc.Request{}, err
	}
	if fs.NArg() != 1 {
		return agentrpc.Request{}, usageErr(fs, "requires exactly one argument: COMMAND")
	}
	params, _ := json.Marshal(agentrpc.BashRequest{
		Command: fs.Arg(0), Session: *session,
		TimeoutMS: durationMS(*timeout), IdleWaitMS: durationMS(*idleWait),
		Background: *background, Interactive: *interactive, Stdin: *stdin,
	})
	return agentrpc.Request{Op: agentrpc.OpBash, Params: params}, nil
}

func buildOutput(args []string) (agentrpc.Request, error) {
	fs := newFlagSet("output")
	stream := fs.String("stream", "", "stdout or stderr")
	lines := fs.String("lines", "", "inclusive line range A:B")
	grep := fs.String("grep", "", "filter to lines matching this regex")
	context := fs.Int("context", 0, "lines of context around each grep match")
	mode := fs.String("mode", "", "\"templates\" for clustered log analysis")
	if err := fs.Parse(args); err != nil {
		return agentrpc.Request{}, err
	}
	if fs.NArg() != 1 {
		return agentrpc.Request{}, usageErr(fs, "requires exactly one argument: ID")
	}
	params, _ := json.Marshal(agentrpc.BashOutputRequest{
		ID: fs.Arg(0), Stream: *stream, Lines: *lines, Grep: *grep, Context: *context, Mode: *mode,
	})
	return agentrpc.Request{Op: agentrpc.OpBashOutput, Params: params}, nil
}

func buildInput(args []string) (agentrpc.Request, error) {
	fs := newFlagSet("input")
	if err := fs.Parse(args); err != nil {
		return agentrpc.Request{}, err
	}
	if fs.NArg() != 2 {
		return agentrpc.Request{}, usageErr(fs, "requires exactly two arguments: ID DATA")
	}
	params, _ := json.Marshal(agentrpc.BashInputRequest{ID: fs.Arg(0), Data: fs.Arg(1)})
	return agentrpc.Request{Op: agentrpc.OpBashInput, Params: params}, nil
}

func buildKill(args []string) (agentrpc.Request, error) {
	fs := newFlagSet("kill")
	signal := fs.Int("signal", 0, "signal number (default: SIGTERM)")
	if err := fs.Parse(args); err != nil {
		return agentrpc.Request{}, err
	}
	if fs.NArg() != 1 {
		return agentrpc.Request{}, usageErr(fs, "requires exactly one argument: ID")
	}
	params, _ := json.Marshal(agentrpc.BashKillRequest{ID: fs.Arg(0), Signal: *signal})
	return agentrpc.Request{Op: agentrpc.OpBashKill, Params: params}, nil
}

func buildState(args []string) (agentrpc.Request, error) {
	fs := newFlagSet("state")
	session := fs.String("session", "", "session name (default: \"default\")")
	if err := fs.Parse(args); err != nil {
		return agentrpc.Request{}, err
	}
	if fs.NArg() != 0 {
		return agentrpc.Request{}, usageErr(fs, "takes no positional arguments")
	}
	params, _ := json.Marshal(agentrpc.BashStateRequest{Session: *session})
	return agentrpc.Request{Op: agentrpc.OpBashState, Params: params}, nil
}

func buildHistory(args []string) (agentrpc.Request, error) {
	fs := newFlagSet("history")
	session := fs.String("session", "", "filter by session name")
	command := fs.String("command", "", "filter by command full-text search")
	since := fs.String("since", "", "filter to invocations started at/after this RFC3339 timestamp")
	limit := fs.Int("limit", 0, "max results (default: 100, max: 1000)")
	exit := fs.Int("exit", noExitFilter, "filter by exact exit code")
	if err := fs.Parse(args); err != nil {
		return agentrpc.Request{}, err
	}
	if fs.NArg() != 0 {
		return agentrpc.Request{}, usageErr(fs, "takes no positional arguments")
	}
	request := agentrpc.BashHistoryRequest{Session: *session, Command: *command, Since: *since, Limit: *limit}
	if *exit != noExitFilter {
		request.Exit = exit
	}
	params, _ := json.Marshal(request)
	return agentrpc.Request{Op: agentrpc.OpBashHistory, Params: params}, nil
}

// noExitFilter marks BashHistoryRequest.Exit as unset on the CLI: it's a
// *int on the wire so "no filter" and "filter by 0" are distinguishable,
// but flag.Int needs a concrete sentinel default. -1 never collides with a
// real exit code (those are 0-255) or the process-supervisor's synthetic -1.
const noExitFilter = -1000

func buildReplay(args []string) (agentrpc.Request, error) {
	fs := newFlagSet("replay")
	if err := fs.Parse(args); err != nil {
		return agentrpc.Request{}, err
	}
	if fs.NArg() != 1 {
		return agentrpc.Request{}, usageErr(fs, "requires exactly one argument: ID")
	}
	params, _ := json.Marshal(agentrpc.BashReplayRequest{ID: fs.Arg(0)})
	return agentrpc.Request{Op: agentrpc.OpBashReplay, Params: params}, nil
}

func buildProcesses(args []string) (agentrpc.Request, error) {
	fs := newFlagSet("processes")
	session := fs.String("session", "", "filter by session name")
	if err := fs.Parse(args); err != nil {
		return agentrpc.Request{}, err
	}
	if fs.NArg() != 0 {
		return agentrpc.Request{}, usageErr(fs, "takes no positional arguments")
	}
	params, _ := json.Marshal(agentrpc.BashStateRequest{Session: *session})
	return agentrpc.Request{Op: agentrpc.OpBashProcesses, Params: params}, nil
}

func buildGC(args []string) (agentrpc.Request, error) {
	fs := newFlagSet("gc")
	olderThan := fs.Int("older-than-hours", 0, "remove blobs unreferenced and older than this many hours")
	maxBytes := fs.Int64("max-bytes", 0, "remove oldest unreferenced blobs beyond this total size")
	if err := fs.Parse(args); err != nil {
		return agentrpc.Request{}, err
	}
	if fs.NArg() != 0 {
		return agentrpc.Request{}, usageErr(fs, "takes no positional arguments")
	}
	params, _ := json.Marshal(agentrpc.BashGCRequest{OlderThanHours: *olderThan, MaxBytes: *maxBytes})
	return agentrpc.Request{Op: agentrpc.OpBashGC, Params: params}, nil
}

func buildTemplates(args []string) (agentrpc.Request, error) {
	fs := newFlagSet("templates")
	stream := fs.String("stream", "", "stdout or stderr")
	baseline := fs.Bool("baseline", false, "compare against prior runs of the same command")
	if err := fs.Parse(args); err != nil {
		return agentrpc.Request{}, err
	}
	if fs.NArg() != 1 {
		return agentrpc.Request{}, usageErr(fs, "requires exactly one argument: ID")
	}
	params, _ := json.Marshal(agentrpc.BashTemplatesRequest{ID: fs.Arg(0), Stream: *stream, Baseline: *baseline})
	return agentrpc.Request{Op: agentrpc.OpBashTemplates, Params: params}, nil
}

func buildService(args []string) (agentrpc.Request, error) {
	fs := newFlagSet("service")
	session := fs.String("session", "", "session name")
	timeout := fs.Duration("timeout", 0, "command timeout")
	readyPort := fs.Int("ready-port", 0, "readiness: block until this TCP port accepts a connection")
	readyHost := fs.String("ready-host", "", "readiness: host for -ready-port (default: localhost)")
	readyRegex := fs.String("ready-regex", "", "readiness: block until stdout matches this regex")
	readyTailBytes := fs.Int("ready-tail-bytes", 0, "readiness: bytes of stdout tail to check -ready-regex against")
	readyHTTPURL := fs.String("ready-http-url", "", "readiness: block until GET returns 2xx/3xx")
	readyTimeout := fs.Duration("ready-timeout", 0, "readiness: overall timeout")
	readyPoll := fs.Duration("ready-poll", 0, "readiness: poll interval")
	if err := fs.Parse(args); err != nil {
		return agentrpc.Request{}, err
	}
	if fs.NArg() != 2 {
		return agentrpc.Request{}, usageErr(fs, "requires exactly two arguments: NAME COMMAND")
	}
	request := agentrpc.BashServiceRequest{
		Name: fs.Arg(0), Command: fs.Arg(1), Session: *session, TimeoutMS: durationMS(*timeout),
	}
	if *readyPort > 0 || *readyRegex != "" || *readyHTTPURL != "" {
		request.Readiness = &agentrpc.ReadinessSpec{
			Port: *readyPort, Host: *readyHost, StdoutRegex: *readyRegex, TailBytes: *readyTailBytes,
			HTTPURL: *readyHTTPURL, TimeoutMS: durationMS(*readyTimeout), PollIntervalMS: durationMS(*readyPoll),
		}
	}
	params, _ := json.Marshal(request)
	return agentrpc.Request{Op: agentrpc.OpBashService, Params: params}, nil
}

func buildServiceStatus(args []string) (agentrpc.Request, error) {
	fs := newFlagSet("service-status")
	if err := fs.Parse(args); err != nil {
		return agentrpc.Request{}, err
	}
	if fs.NArg() != 1 {
		return agentrpc.Request{}, usageErr(fs, "requires exactly one argument: NAME")
	}
	params, _ := json.Marshal(agentrpc.BashServiceStatusRequest{Name: fs.Arg(0)})
	return agentrpc.Request{Op: agentrpc.OpBashServiceStatus, Params: params}, nil
}

func buildServiceKill(args []string) (agentrpc.Request, error) {
	fs := newFlagSet("service-kill")
	signal := fs.Int("signal", 0, "signal number (default: SIGTERM)")
	if err := fs.Parse(args); err != nil {
		return agentrpc.Request{}, err
	}
	if fs.NArg() != 1 {
		return agentrpc.Request{}, usageErr(fs, "requires exactly one argument: NAME")
	}
	params, _ := json.Marshal(agentrpc.BashServiceKillRequest{Name: fs.Arg(0), Signal: *signal})
	return agentrpc.Request{Op: agentrpc.OpBashServiceKill, Params: params}, nil
}

func buildServiceLogs(args []string) (agentrpc.Request, error) {
	fs := newFlagSet("service-logs")
	stream := fs.String("stream", "", "stdout or stderr")
	lines := fs.String("lines", "", "inclusive line range A:B")
	tail := fs.Int("tail", 0, "return only the last N lines")
	grep := fs.String("grep", "", "filter to lines matching this regex")
	context := fs.Int("context", 0, "lines of context around each grep match")
	follow := fs.Bool("follow", false, "block collecting new output until idle/timeout/max-lines")
	followIdle := fs.Duration("follow-idle", 0, "follow: stop after this long with nothing new")
	followMaxLines := fs.Int("follow-max-lines", 0, "follow: stop after collecting this many new lines")
	followTimeout := fs.Duration("follow-timeout", 0, "follow: overall cap")
	if err := fs.Parse(args); err != nil {
		return agentrpc.Request{}, err
	}
	if fs.NArg() != 1 {
		return agentrpc.Request{}, usageErr(fs, "requires exactly one argument: NAME")
	}
	params, _ := json.Marshal(agentrpc.BashServiceLogsRequest{
		Name: fs.Arg(0), Stream: *stream, Lines: *lines, Tail: *tail, Grep: *grep, Context: *context,
		Follow: *follow, FollowIdleMS: durationMS(*followIdle), FollowMaxLines: *followMaxLines,
		FollowTimeoutMS: durationMS(*followTimeout),
	})
	return agentrpc.Request{Op: agentrpc.OpBashServiceLogs, Params: params}, nil
}

func durationMS(d time.Duration) int64 {
	return d.Milliseconds()
}

func usageErr(fs *flag.FlagSet, message string) error {
	return fmt.Errorf("%s: %s", fs.Name(), message)
}

// --- daemon lifecycle -----------------------------------------------------

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

// unavailable reports whether err means "nothing is listening at the
// socket" (connection refused, or no socket file at all) as opposed to some
// other dial failure. A permission error or a socket owned by another user
// surfaces as a different errno and must not trigger a spurious daemon spawn.
func unavailable(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
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
