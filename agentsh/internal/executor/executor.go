package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/output"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/session"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/storage"
)

const (
	DefaultTimeout  = 120 * time.Second
	DefaultGrace    = 5 * time.Second
	DefaultIdleWait = 10 * time.Second

	// maxControlBytes bounds the fd-3 "control pipe" used to capture shell
	// state (cwd/env) after a command runs. It should only ever carry a few
	// KB; the cap guards against a command that writes to fd 3 (deliberately
	// or by accident) growing daemon heap without limit.
	maxControlBytes = 4 * 1024 * 1024 // 4MB
)

var hygiene = map[string]string{
	"TERM": "dumb", "NO_COLOR": "1", "CLICOLOR": "0", "PAGER": "cat",
	"GIT_PAGER": "cat", "LESS": "FRX", "DEBIAN_FRONTEND": "noninteractive",
	"CI": "1", "PYTHONUNBUFFERED": "1",
}

type Store interface {
	CreateInvocation(context.Context, storage.Invocation) error
	SetPID(context.Context, string, int) error
	FinishInvocation(context.Context, storage.Invocation) error
}

type Request struct {
	Command     string
	Session     string
	CWD         string
	Timeout     time.Duration
	IdleTimeout time.Duration
	Background  bool
	Interactive bool
	Stdin       string
}

type Executor struct {
	Store        Store
	Blobs        storage.BlobStore
	Sessions     *session.Manager
	Containment  Containment
	Grace        time.Duration
	IdleWait     time.Duration
	MaxTimeout   time.Duration
	LoopDetector *LoopDetector

	// baseEnv is the environment commands inherit. It is the comparison point
	// for a fresh session's first command; without it every inherited variable
	// reads as a change that command made.
	baseEnv map[string]string

	mu      sync.Mutex
	running map[string]*run
}

type run struct {
	invocation storage.Invocation
	cmd        *exec.Cmd
	stdinPipe  io.WriteCloser
	control    *bytes.Buffer
	// controlTruncated is set by the control-pipe copy goroutine before
	// controlWg is released, so reading it after controlWg.Wait() is safe
	// without a lock.
	controlTruncated bool
	controlWg        sync.WaitGroup
	stdout           *storage.BlobWriter
	stderr           *storage.BlobWriter
	cancel           context.CancelFunc
	done             chan struct{}
	inputCh          chan struct{}

	// lastOutput is unix nanos of the most recent write on either stream. It is
	// atomic because the output goroutines record it while the waiter reads it.
	lastOutput atomic.Int64
}

// activityWriter timestamps writes so the idle check measures silence rather
// than elapsed runtime. Without it a chatty long-running command looks idle.
type activityWriter struct {
	dest io.Writer
	last *atomic.Int64
}

func (w activityWriter) Write(data []byte) (int, error) {
	n, err := w.dest.Write(data)
	if n > 0 {
		w.last.Store(time.Now().UnixNano())
	}
	return n, err
}

func New(store Store, blobs storage.BlobStore) *Executor {
	return &Executor{
		Store: store, Blobs: blobs, Sessions: session.NewManager(), Containment: DefaultContainment(),
		Grace: DefaultGrace, IdleWait: DefaultIdleWait, LoopDetector: NewLoopDetector(DefaultLoopConfig()),
		baseEnv: environmentMap(),
		running: make(map[string]*run),
	}
}

func (e *Executor) Execute(ctx context.Context, request Request) (storage.Invocation, error) {
	if strings.TrimSpace(request.Command) == "" {
		return storage.Invocation{}, errors.New("command is required")
	}
	if request.Session == "" {
		request.Session = "default"
	}
	if request.CWD == "" {
		request.CWD = "."
	}
	timeout := request.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if e.MaxTimeout > 0 && timeout > e.MaxTimeout {
		timeout = e.MaxTimeout
	}
	// Defaults are resolved without writing to the receiver: concurrent Execute
	// calls would otherwise race on these fields.
	if e.Containment == nil {
		e.Containment = Null{}
	}
	idleWait := request.IdleTimeout
	if idleWait <= 0 {
		idleWait = e.IdleWait
	}
	if idleWait <= 0 {
		idleWait = DefaultIdleWait
	}

	invocation := storage.Invocation{
		ID: newID(), Session: request.Session, Argv: []string{"bash", "--noprofile", "--norc", "-c", request.Command},
		Command: request.Command, CWD: request.CWD, State: storage.StateRunning, StartedAt: time.Now().UTC(),
	}
	if err := e.Store.CreateInvocation(ctx, invocation); err != nil {
		return storage.Invocation{}, err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	r := &run{
		invocation: invocation, cancel: cancel, done: make(chan struct{}),
		inputCh: make(chan struct{}, 1),
	}
	r.lastOutput.Store(time.Now().UnixNano())
	if err := e.start(runCtx, r, request); err != nil {
		cancel()
		now := time.Now().UTC()
		reason := startReason(err)
		r.invocation.State, r.invocation.EndedAt, r.invocation.Reason = storage.StateExited, &now, &reason
		r.invocation.DurationMS = now.Sub(r.invocation.StartedAt).Milliseconds()
		_ = e.Store.FinishInvocation(context.Background(), r.invocation)
		return r.invocation, err
	}
	e.mu.Lock()
	e.running[invocation.ID] = r
	e.mu.Unlock()
	// Copy the initial record before the waiter starts mutating terminal fields.
	// Background callers receive this durable running snapshot.
	backgroundSnapshot := r.invocation
	go e.wait(r, timeout, idleWait)
	if request.Background {
		return backgroundSnapshot, nil
	}
	select {
	case <-r.done:
		return r.invocation, nil
	case <-r.inputCh:
		// Process is waiting on input; return promptly to the agent with recovery affordances
		e.mu.Lock()
		snapshot := r.invocation
		e.mu.Unlock()
		return snapshot, nil
	case <-ctx.Done():
		_ = e.Kill(r.invocation.ID, syscall.SIGTERM)
		<-r.done
		return r.invocation, ctx.Err()
	}
}

func (e *Executor) start(ctx context.Context, r *run, request Request) (err error) {
	stdout, err := e.Blobs.NewWriter()
	if err != nil {
		return err
	}
	stderr, err := e.Blobs.NewWriter()
	if err != nil {
		stdout.Discard()
		return err
	}
	// Every failure below this point must release both writers. Each holds an
	// open temp file, so leaking them costs two descriptors and one stray
	// .stream-* file per failed start, forever.
	started := false
	defer func() {
		if !started {
			stdout.Discard()
			stderr.Discard()
		}
	}()

	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		return err
	}

	sessionState := e.Sessions.Get(request.Session, request.CWD)
	script := "set -o pipefail\n"
	if prelude, ok := e.Containment.(CommandPrelude); ok {
		script += prelude.Prelude(r.invocation.ID)
	}
	script += sessionState.BuildPrelude()
	script += request.Command + "\n"
	script += session.StateCaptureScript()

	r.control = new(bytes.Buffer)
	r.controlWg.Add(1)
	go func() {
		defer r.controlWg.Done()
		n, _ := io.CopyN(r.control, controlRead, maxControlBytes)
		if n == maxControlBytes {
			// The child is still writing to fd 3; drain and discard the rest
			// so it doesn't block on a full pipe, and flag the capture as
			// incomplete instead of silently truncating it.
			_, _ = io.Copy(io.Discard, controlRead)
			r.controlTruncated = true
		}
		_ = controlRead.Close()
	}()

	cmd := exec.CommandContext(ctx, "bash", "--noprofile", "--norc", "-c", script)
	cmd.Dir, cmd.Env = request.CWD, cleanEnvironment()
	cmd.Stdout = activityWriter{dest: stdout, last: &r.lastOutput}
	cmd.Stderr = activityWriter{dest: stderr, last: &r.lastOutput}
	cmd.ExtraFiles = []*os.File{controlWrite}
	if request.Interactive {
		stdinPipe, pipeErr := cmd.StdinPipe()
		if pipeErr != nil {
			controlWrite.Close()
			return pipeErr
		}
		r.stdinPipe = stdinPipe
	} else if request.Stdin != "" {
		cmd.Stdin = strings.NewReader(request.Stdin)
	} else {
		null, openErr := os.Open(os.DevNull)
		if openErr != nil {
			controlWrite.Close()
			return openErr
		}
		defer null.Close()
		cmd.Stdin = null
	}
	e.Containment.Configure(cmd)
	if preparer, ok := e.Containment.(ProcessPreparer); ok {
		if err := preparer.Prepare(r.invocation.ID, cmd); err != nil {
			controlWrite.Close()
			return fmt.Errorf("prepare process containment: %w", err)
		}
	}
	if err := cmd.Start(); err != nil {
		controlWrite.Close()
		return err
	}
	controlWrite.Close()
	r.cmd, r.stdout, r.stderr = cmd, stdout, stderr
	pid := cmd.Process.Pid
	r.invocation.PID = &pid
	if lifecycle, ok := e.Containment.(ProcessLifecycle); ok {
		if err := lifecycle.Started(r.invocation.ID, cmd.Process); err != nil {
			_ = ProcessGroup{}.Signal(cmd.Process, syscall.SIGKILL)
			_, _ = cmd.Process.Wait()
			return fmt.Errorf("attach process containment: %w", err)
		}
	}
	started = true
	return e.Store.SetPID(context.Background(), r.invocation.ID, pid)
}

func (e *Executor) wait(r *run, timeout, idleWait time.Duration) {
	defer close(r.done)
	wait := make(chan error, 1)
	go func() { wait <- r.cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var err error
	var waitingTriggered bool

waitLoop:
	for {
		select {
		case err = <-wait:
			break waitLoop
		case <-timer.C:
			e.mu.Lock()
			r.invocation.State = storage.StateTimeout
			e.mu.Unlock()
			err = e.terminate(r, wait)
			break waitLoop
		case <-ticker.C:
			if !waitingTriggered && r.stdinPipe != nil && r.cmd != nil && r.cmd.Process != nil {
				idle := time.Since(time.Unix(0, r.lastOutput.Load()))
				if idle >= idleWait && isWaitingOnStdin(r.cmd.Process.Pid) {
					waitingTriggered = true
					e.mu.Lock()
					r.invocation.State = storage.StateWaitingOnInput
					reason := "waiting_on_input"
					r.invocation.Reason = &reason
					r.invocation.Stdout.Preview = fmt.Sprintf("[waiting on input for %ds — process is reading stdin]\n→ BashInput(id=\"%s\", data=\"...\")\n→ BashKill(id=\"%s\")",
						int(idleWait.Seconds()), r.invocation.ID, r.invocation.ID)
					e.mu.Unlock()
					select {
					case r.inputCh <- struct{}{}:
					default:
					}
				}
			}
		}
	}
	now := time.Now().UTC()
	r.controlWg.Wait()
	baseline := e.Sessions.Get(r.invocation.Session, r.invocation.CWD)
	if len(baseline.Env) == 0 && e.baseEnv != nil {
		// A session with no recorded environment yet would otherwise report
		// every inherited variable as this command's doing.
		baseline.Env = e.baseEnv
	}
	newState, envDelta := session.ParseCapturedState(r.control.Bytes(), baseline)

	e.mu.Lock()
	if newState.CWD != "" {
		e.Sessions.Set(r.invocation.Session, newState)
		cwd := newState.CWD
		r.invocation.CWDAfter = &cwd
	}
	r.invocation.EnvAfter = newState.Env
	r.invocation.EnvDelta = envDelta
	r.invocation.EndedAt = &now
	r.invocation.DurationMS = now.Sub(r.invocation.StartedAt).Milliseconds()
	if r.invocation.State != storage.StateTimeout && r.invocation.State != storage.StateKilled {
		r.invocation.State = storage.StateExited
	}
	e.mapExit(&r.invocation, err)
	e.mu.Unlock()
	var oomReason *string
	if lifecycle, ok := e.Containment.(ProcessLifecycle); ok {
		if lifecycle.OOMKilled(r.invocation.ID) {
			reason := "oom"
			oomReason = &reason
		}
		defer lifecycle.Cleanup(r.invocation.ID)
	}

	// Finalize the streams into locals first. Committing and previewing touches
	// the filesystem, and holding the lock across it would stall Processes and
	// Kill for the length of an I/O pass.
	invocationID := r.invocation.ID
	stdoutRef, stdoutText := e.finalizeStream(r.stdout, invocationID, "stdout")
	stderrRef, stderrText := e.finalizeStream(r.stderr, invocationID, "stderr")

	e.mu.Lock()
	if oomReason != nil {
		r.invocation.Reason = oomReason
	}
	r.invocation.Stdout, r.invocation.Stderr = stdoutRef, stderrRef
	exitCode := 0
	if r.invocation.ExitCode != nil {
		exitCode = *r.invocation.ExitCode
	}
	r.invocation.Summary = output.FormatCommand(r.invocation.Command, stdoutText, stderrText, exitCode)
	if r.controlTruncated {
		r.invocation.Stdout.Preview += fmt.Sprintf("\n[warning: shell state capture exceeded %dMB and was truncated; cwd/env delta may be incomplete]", maxControlBytes/(1024*1024))
	}
	if r.invocation.Reason != nil && *r.invocation.Reason != "ok" {
		r.invocation.Stdout.Preview += recoveryHint(r.invocation)
	}
	// Copy before releasing the lock; everything below reads a stable record.
	finished := r.invocation
	e.mu.Unlock()

	if e.LoopDetector != nil {
		if finished.Reason != nil && *finished.Reason != "ok" {
			if warn := e.LoopDetector.RecordAndCheck(finished); warn != "" {
				finished.Stdout.Preview += "\n" + warn
				e.mu.Lock()
				r.invocation.Stdout.Preview = finished.Stdout.Preview
				e.mu.Unlock()
			}
		} else {
			e.LoopDetector.RecordSuccess(finished)
		}
	}

	_ = e.Store.FinishInvocation(context.Background(), finished)
	e.mu.Lock()
	delete(e.running, invocationID)
	e.mu.Unlock()
	r.cancel()
}

// finalizeStream commits a stream and builds its preview and summary excerpt.
func (e *Executor) finalizeStream(writer *storage.BlobWriter, invocationID, name string) (storage.StreamRef, string) {
	ref, err := writer.Commit()
	if err != nil {
		return storage.StreamRef{}, ""
	}
	if file, openErr := e.Blobs.Open(ref.SHA256); openErr == nil {
		preview, previewErr := output.Preview(file, invocationID, name)
		file.Close()
		if previewErr == nil {
			ref.Preview, ref.Truncated = preview.Preview, preview.Truncated
		}
	}
	return ref, readBlobExcerpt(e.Blobs, ref.SHA256, 512*1024)
}

func (e *Executor) grace() time.Duration {
	if e.Grace <= 0 {
		return DefaultGrace
	}
	return e.Grace
}

func (e *Executor) terminate(r *run, wait <-chan error) error {
	_ = e.Containment.Signal(r.cmd.Process, syscall.SIGTERM)
	select {
	case err := <-wait:
		return err
	case <-time.After(e.grace()):
		var killErr error
		if killer, ok := e.Containment.(WholeTreeKiller); ok {
			killErr = killer.KillInvocation(r.invocation.ID)
		} else {
			killErr = e.Containment.Signal(r.cmd.Process, syscall.SIGKILL)
		}
		if killErr != nil {
			return killErr
		}
		return <-wait
	}
}

func (e *Executor) Processes(sessionName string) []storage.Invocation {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([]storage.Invocation, 0)
	for _, r := range e.running {
		if sessionName == "" || r.invocation.Session == sessionName {
			result = append(result, r.invocation)
		}
	}
	return result
}

func (e *Executor) Replay(ctx context.Context, invocation storage.Invocation) (storage.Invocation, error) {
	return e.Execute(ctx, Request{Command: invocation.Command, Session: invocation.Session, CWD: invocation.CWD})
}

func (e *Executor) WriteInput(id string, data []byte) error {
	e.mu.Lock()
	r := e.running[id]
	e.mu.Unlock()
	if r == nil {
		return fmt.Errorf("invocation %q is not running", id)
	}
	if r.stdinPipe == nil {
		return fmt.Errorf("invocation %q does not accept interactive stdin", id)
	}
	_, err := r.stdinPipe.Write(data)
	return err
}

func (e *Executor) OpenRunning(id, stream string) (io.ReadCloser, bool, error) {
	e.mu.Lock()
	r := e.running[id]
	e.mu.Unlock()
	if r == nil {
		return nil, false, nil
	}
	var writer *storage.BlobWriter
	switch stream {
	case "stdout":
		writer = r.stdout
	case "stderr":
		writer = r.stderr
	default:
		return nil, true, errors.New("stream must be stdout or stderr")
	}
	file, err := writer.OpenSnapshot()
	return file, true, err
}

func (e *Executor) Kill(id string, signal syscall.Signal) error {
	e.mu.Lock()
	r := e.running[id]
	if r != nil {
		r.invocation.State = storage.StateKilled
	}
	e.mu.Unlock()
	if r == nil {
		return fmt.Errorf("invocation %q is not running", id)
	}
	if signal == 0 {
		signal = syscall.SIGTERM
	}
	return e.Containment.Signal(r.cmd.Process, signal)
}

func (e *Executor) mapExit(inv *storage.Invocation, err error) {
	reason := "ok"
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			reason = "nonzero"
			code = exit.ExitCode()
			if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				signal := int(status.Signal())
				inv.Signal = &signal
				reason = "signal"
			}
		} else {
			// Not the command's exit status — the supervisor itself failed.
			// Reporting "exit 1" here invents a result the command never
			// produced and sends the agent debugging the wrong thing.
			reason = "supervisor_error"
			code = -1
		}
	}
	if inv.State == storage.StateTimeout {
		reason = "timeout"
	}
	if inv.State == storage.StateKilled {
		reason = "killed"
	}
	inv.ExitCode, inv.Reason = &code, &reason
}

// withheld names variables that must never reach a supervised command. The
// daemon takes its own settings from a config file, so these carry no meaning
// for children — but an operator's shell may still hold them, and a credential
// handed to arbitrary agent-run processes is a credential leaked.
var withheld = map[string]bool{
	"TURSO_AUTH_TOKEN":   true,
	"TURSO_DATABASE_URL": true,
}

// environmentMap is the environment a supervised command inherits.
func environmentMap() map[string]string {
	values := map[string]string{}
	for _, item := range os.Environ() {
		if key, value, ok := strings.Cut(item, "="); ok {
			if withheld[key] {
				continue
			}
			values[key] = value
		}
	}
	for key, value := range hygiene {
		values[key] = value
	}
	return values
}

func cleanEnvironment() []string {
	values := environmentMap()
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func recoveryHint(inv storage.Invocation) string {
	hint := fmt.Sprintf("\n[%s — exit %d]\n→ BashOutput(id=\"%s\", stream=\"stderr\")", inv.State, valueOr(inv.ExitCode, -1), inv.ID)
	if inv.State == storage.StateTimeout {
		hint += fmt.Sprintf("\n→ BashOutput(id=\"%s\")\n→ Bash(command=\"%s\", timeout=600)", inv.ID, strings.ReplaceAll(inv.Command, "\"", "\\\""))
	}
	return hint
}

func valueOr(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func startReason(err error) string {
	if errors.Is(err, exec.ErrNotFound) {
		return "not_found"
	}
	if errors.Is(err, os.ErrPermission) {
		return "permission"
	}
	return "nonzero"
}

func readBlobExcerpt(blobs storage.BlobStore, sha string, maxBytes int64) string {
	if sha == "" {
		return ""
	}
	file, err := blobs.Open(sha)
	if err != nil {
		return ""
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes))
	if err != nil {
		return ""
	}
	return string(data)
}

func newID() string {
	var b [4]byte
	_, _ = io.ReadFull(rand.Reader, b[:])
	return "inv_" + hex.EncodeToString(b[:])
}
