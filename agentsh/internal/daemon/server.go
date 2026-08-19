package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Fewbytes/sweatshop/agentsh/internal/analyzer"
	"github.com/Fewbytes/sweatshop/agentsh/internal/config"
	"github.com/Fewbytes/sweatshop/agentsh/internal/executor"
	"github.com/Fewbytes/sweatshop/agentsh/internal/output"
	agentrpc "github.com/Fewbytes/sweatshop/agentsh/internal/rpc"
	"github.com/Fewbytes/sweatshop/agentsh/internal/service"
	"github.com/Fewbytes/sweatshop/agentsh/internal/storage"
	"github.com/Fewbytes/sweatshop/agentsh/internal/version"
	"github.com/Fewbytes/sweatshop/agentsh/internal/workspace"
)

type Server struct {
	paths workspace.Paths

	// Config holds daemon settings. It comes from a file rather than the
	// environment: the daemon's environment is inherited by every supervised
	// command, so credentials must not live there.
	Config config.Config

	store *storage.Store
	exec  *executor.Executor
	// blobs is constructed once in Serve rather than inline at each call
	// site — it's a plain value wrapping two paths, cheap to build, but
	// there's no reason for four call sites to each build their own copy.
	blobs storage.BlobStore
	stop  chan struct{}
	once  sync.Once
	wg    sync.WaitGroup

	// Grace overrides the executor's kill grace period (SIGTERM-to-SIGKILL
	// wait) when > 0. Set before calling Serve; tests use it to keep
	// cancellation/shutdown assertions fast without racing the executor
	// field itself.
	Grace time.Duration
}

func New(paths workspace.Paths) *Server {
	return &Server{paths: paths, stop: make(chan struct{})}
}

func NewWithConfig(paths workspace.Paths, settings config.Config) *Server {
	return &Server{paths: paths, Config: settings, stop: make(chan struct{})}
}

func (s *Server) Serve(ctx context.Context) error {
	if err := s.paths.Ensure(); err != nil {
		return err
	}
	// Claim the socket before opening storage. The listener is the singleton
	// guard: opening the database first lets a second daemon reach the libSQL
	// file and die on "database is locked" before listen() ever gets to report
	// "daemon already running", turning a clear conflict into a cryptic one.
	ln, err := s.listen()
	if err != nil {
		return err
	}
	defer s.cleanup()

	for _, warning := range s.Config.Warnings {
		fmt.Fprintln(os.Stderr, "agentshd:", warning)
	}
	syncInterval := s.Config.SyncInterval()
	if syncInterval == 0 && s.Config.Turso.URL != "" {
		syncInterval = time.Minute
	}
	store, err := storage.Open(ctx, storage.Config{
		Path: s.paths.Database, RemoteURL: s.Config.Turso.URL,
		AuthToken: s.Config.Turso.AuthToken, SyncInterval: syncInterval,
	})
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("open invocation storage: %w", err)
	}
	s.store = store
	s.blobs = storage.BlobStore{Root: s.paths.Blobs, Index: s.paths.Index}
	s.exec = executor.New(store, s.blobs)
	if s.Grace > 0 {
		s.exec.Grace = s.Grace
	}
	defer store.Close()
	if _, err := store.Reconcile(ctx, processAlive); err != nil {
		_ = ln.Close()
		return fmt.Errorf("reconcile invocations: %w", err)
	}
	if err := s.exec.LoadServices(s.paths.Services, processAlive); err != nil {
		_ = ln.Close()
		return fmt.Errorf("load services: %w", err)
	}

	// Close the listener when shutdown is signaled so Accept unblocks. The
	// listener is owned by this goroutine, so there is no shared mutable
	// listener state to race with Shutdown.
	serveDone := make(chan struct{})
	defer close(serveDone)
	go func() {
		select {
		case <-s.stop:
			_ = ln.Close()
		case <-serveDone:
		}
	}()

	sigCtx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// serverCtx is the parent for every in-flight handler. It ends the
	// moment shutdown is signaled (by SIGINT/SIGTERM or an OpShutdown RPC),
	// so a foreground command started before shutdown gets cancelled instead
	// of blocking Shutdown/s.wg.Wait() indefinitely.
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()
	go func() {
		select {
		case <-sigCtx.Done():
			s.Shutdown()
		case <-s.stop:
		}
		serverCancel()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.stop:
				s.wg.Wait()
				return nil
			default:
				return err
			}
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn, serverCtx)
		}()
	}
}

func (s *Server) listen() (net.Listener, error) {
	ln, err := net.Listen("unix", s.paths.Socket)
	if err != nil && errors.Is(err, syscall.EADDRINUSE) {
		conn, dialErr := net.Dial("unix", s.paths.Socket)
		if dialErr == nil {
			conn.Close()
			return nil, fmt.Errorf("daemon already running at %s", s.paths.Socket)
		}
		if removeErr := os.Remove(s.paths.Socket); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return nil, removeErr
		}
		ln, err = net.Listen("unix", s.paths.Socket)
	}
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(s.paths.Socket, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	if err := os.WriteFile(s.paths.PID, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func (s *Server) handle(conn net.Conn, serverCtx context.Context) {
	defer conn.Close()
	var request agentrpc.Request
	if err := json.NewDecoder(io.LimitReader(conn, 1<<20)).Decode(&request); err != nil {
		_ = json.NewEncoder(conn).Encode(agentrpc.Failure("", "invalid_request", err.Error()))
		return
	}
	if request.Version != agentrpc.Version {
		_ = json.NewEncoder(conn).Encode(agentrpc.Failure(request.ID, "version", "unsupported protocol version"))
		return
	}

	// ctx ends when the server shuts down or this client disconnects,
	// whichever comes first. The request body is already fully decoded
	// above, so it's safe to have a goroutine read from conn concurrently:
	// any further read only ever observes EOF/error when the peer goes
	// away (net.Conn allows concurrent Read/Write from different
	// goroutines). executor.Execute treats a background invocation
	// independently of this ctx, so disconnecting doesn't kill it.
	ctx, cancel := context.WithCancel(serverCtx)
	defer cancel()
	go func() {
		var probe [1]byte
		_, _ = conn.Read(probe[:])
		cancel()
	}()

	response, after := s.dispatch(ctx, request)
	_ = json.NewEncoder(conn).Encode(response)
	// Only OpShutdown sets this: it must run after the response is on the
	// wire, not before, so the client's "stopping" ack isn't racing socket
	// teardown.
	if after != nil {
		after()
	}
}

// opHandler serves one RPC op. The returned func, when non-nil, runs after
// the response has been written to the connection — the only user is
// OpShutdown, which must not tear the socket down before its own ack is sent.
type opHandler func(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func())

var handlers = map[string]opHandler{
	agentrpc.OpHealth:            handleHealth,
	agentrpc.OpShutdown:          handleShutdown,
	agentrpc.OpBash:              handleBash,
	agentrpc.OpBashService:       handleBashService,
	agentrpc.OpBashServiceStatus: handleBashServiceStatus,
	agentrpc.OpBashServiceKill:   handleBashServiceKill,
	agentrpc.OpBashServiceLogs:   handleBashServiceLogs,
	agentrpc.OpBashOutput:        handleBashOutput,
	agentrpc.OpBashInput:         handleBashInput,
	agentrpc.OpBashKill:          handleBashKill,
	agentrpc.OpBashState:         handleBashState,
	agentrpc.OpBashProcesses:     handleBashProcesses,
	agentrpc.OpBashHistory:       handleBashHistory,
	agentrpc.OpBashReplay:        handleBashReplay,
	agentrpc.OpBashGC:            handleBashGC,
	agentrpc.OpBashTemplates:     handleBashTemplates,
}

func (s *Server) dispatch(ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	h, ok := handlers[request.Op]
	if !ok {
		return agentrpc.Failure(request.ID, "unknown_operation", request.Op), nil
	}
	return h(s, ctx, request)
}

// decodeParams unmarshals request.Params into T, matching how every op
// handled it before this table existed: empty params is itself a decode
// error (most request types have required fields). Ops where params are
// genuinely optional (state/processes/gc) use decodeOptionalParams instead.
func decodeParams[T any](raw json.RawMessage) (T, error) {
	var v T
	err := json.Unmarshal(raw, &v)
	return v, err
}

// decodeOptionalParams treats missing params as the zero value rather than
// a decode error, but — unlike the three ops this replaces used to — still
// reports genuinely malformed (non-empty, invalid) params as an error
// instead of silently discarding it.
func decodeOptionalParams[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 {
		return v, nil
	}
	err := json.Unmarshal(raw, &v)
	return v, err
}

func invalidParams(request agentrpc.Request, err error) (agentrpc.Response, func()) {
	return agentrpc.Failure(request.ID, "invalid_params", err.Error()), nil
}

func handleHealth(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	return agentrpc.Success(request.ID, agentrpc.Health{
		Status: "ok", PID: os.Getpid(), Workspace: s.paths.Root, Version: version.String(),
	}), nil
}

func handleShutdown(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	return agentrpc.Success(request.ID, map[string]string{"status": "stopping"}), s.Shutdown
}

func handleBash(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	params, err := decodeParams[agentrpc.BashRequest](request.Params)
	if err != nil {
		return invalidParams(request, err)
	}
	invocation, err := s.exec.Execute(ctx, executor.Request{
		Command: params.Command, Session: params.Session, CWD: s.paths.Root,
		Timeout:     time.Duration(params.TimeoutMS) * time.Millisecond,
		IdleTimeout: time.Duration(params.IdleWaitMS) * time.Millisecond,
		Background:  params.Background,
		Interactive: params.Interactive,
		Stdin:       params.Stdin,
	})
	if err != nil {
		return agentrpc.Failure(request.ID, "execution", err.Error()), nil
	}
	return agentrpc.Success(request.ID, invocation.View()), nil
}

func handleBashService(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	params, err := decodeParams[agentrpc.BashServiceRequest](request.Params)
	if err != nil {
		return invalidParams(request, err)
	}
	svc, err := s.exec.StartService(ctx, executor.ServiceRequest{
		Name: params.Name, Command: params.Command, Session: params.Session,
		CWD: s.paths.Root, Timeout: time.Duration(params.TimeoutMS) * time.Millisecond,
		Readiness: readinessSpec(params.Readiness),
	})
	if err != nil {
		return agentrpc.Failure(request.ID, "service", err.Error()), nil
	}
	return agentrpc.Success(request.ID, svc), nil
}

func handleBashServiceStatus(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	params, err := decodeParams[agentrpc.BashServiceStatusRequest](request.Params)
	if err != nil {
		return invalidParams(request, err)
	}
	svc, err := s.exec.ServiceStatus(ctx, params.Name)
	if err != nil {
		return agentrpc.Failure(request.ID, "service_status", err.Error()), nil
	}
	return agentrpc.Success(request.ID, svc), nil
}

func handleBashServiceKill(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	params, err := decodeParams[agentrpc.BashServiceKillRequest](request.Params)
	if err != nil {
		return invalidParams(request, err)
	}
	sig := syscall.Signal(params.Signal)
	if sig == 0 {
		sig = syscall.SIGTERM
	}
	if err := s.exec.KillService(params.Name, sig); err != nil {
		return agentrpc.Failure(request.ID, "service_kill", err.Error()), nil
	}
	return agentrpc.Success(request.ID, map[string]string{"status": "killed"}), nil
}

func handleBashServiceLogs(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	params, err := decodeParams[agentrpc.BashServiceLogsRequest](request.Params)
	if err != nil {
		return invalidParams(request, err)
	}
	result, err := s.serviceLogs(ctx, params)
	if err != nil {
		return agentrpc.Failure(request.ID, "service_logs", err.Error()), nil
	}
	return agentrpc.Success(request.ID, result), nil
}

func handleBashOutput(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	params, err := decodeParams[agentrpc.BashOutputRequest](request.Params)
	if err != nil {
		return invalidParams(request, err)
	}
	result, err := s.readOutput(ctx, params)
	if err != nil {
		return agentrpc.Failure(request.ID, "output", err.Error()), nil
	}
	return agentrpc.Success(request.ID, result), nil
}

func handleBashInput(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	params, err := decodeParams[agentrpc.BashInputRequest](request.Params)
	if err != nil {
		return invalidParams(request, err)
	}
	if err := s.exec.WriteInput(params.ID, []byte(params.Data)); err != nil {
		return agentrpc.Failure(request.ID, "input", err.Error()), nil
	}
	return agentrpc.Success(request.ID, map[string]string{"status": "written"}), nil
}

func handleBashKill(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	params, err := decodeParams[agentrpc.BashKillRequest](request.Params)
	if err != nil {
		return invalidParams(request, err)
	}
	sig := syscall.Signal(params.Signal)
	if sig == 0 {
		sig = syscall.SIGTERM
	}
	if err := s.exec.Kill(params.ID, sig); err != nil {
		return agentrpc.Failure(request.ID, "kill", err.Error()), nil
	}
	return agentrpc.Success(request.ID, map[string]string{"status": "killed"}), nil
}

func handleBashState(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	params, err := decodeOptionalParams[agentrpc.BashStateRequest](request.Params)
	if err != nil {
		return invalidParams(request, err)
	}
	if params.Session == "" {
		params.Session = "default"
	}
	state := s.exec.Sessions.Get(params.Session, s.paths.Root)
	return agentrpc.Success(request.ID, state), nil
}

func handleBashProcesses(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	params, err := decodeOptionalParams[agentrpc.BashStateRequest](request.Params)
	if err != nil {
		return invalidParams(request, err)
	}
	return agentrpc.Success(request.ID, storage.Views(s.exec.Processes(params.Session))), nil
}

func handleBashHistory(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	params, err := decodeParams[agentrpc.BashHistoryRequest](request.Params)
	if err != nil {
		return invalidParams(request, err)
	}
	history, err := s.store.History(ctx, params.Session, params.Command, params.Since, params.Exit, params.Limit)
	if err != nil {
		return agentrpc.Failure(request.ID, "history", err.Error()), nil
	}
	return agentrpc.Success(request.ID, storage.Views(history)), nil
}

func handleBashReplay(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	params, err := decodeParams[agentrpc.BashReplayRequest](request.Params)
	if err != nil {
		return invalidParams(request, err)
	}
	original, err := s.store.GetInvocation(ctx, params.ID)
	if err != nil {
		return agentrpc.Failure(request.ID, "replay", err.Error()), nil
	}
	replayed, err := s.exec.Replay(ctx, original)
	if err != nil {
		return agentrpc.Failure(request.ID, "replay", err.Error()), nil
	}
	return agentrpc.Success(request.ID, replayed.View()), nil
}

func handleBashGC(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	params, err := decodeOptionalParams[agentrpc.BashGCRequest](request.Params)
	if err != nil {
		return invalidParams(request, err)
	}
	gc, err := s.blobs.GC(time.Duration(params.OlderThanHours)*time.Hour, params.MaxBytes)
	if err != nil {
		return agentrpc.Failure(request.ID, "gc", err.Error()), nil
	}
	return agentrpc.Success(request.ID, gc), nil
}

func handleBashTemplates(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	params, err := decodeParams[agentrpc.BashTemplatesRequest](request.Params)
	if err != nil {
		return invalidParams(request, err)
	}
	analysis, err := s.readTemplates(ctx, params)
	if err != nil {
		return agentrpc.Failure(request.ID, "templates", err.Error()), nil
	}
	return agentrpc.Success(request.ID, analysis), nil
}

// serviceLogs resolves a service name to its (current or last) invocation
// and pages its output through the same infrastructure BashOutput uses.
// Cross-restart concatenation is intentionally out of scope: a Service
// record only tracks its current invocation ID (see executor/service.go),
// so logs from a prior run under the same name aren't linked here.
func (s *Server) serviceLogs(ctx context.Context, params agentrpc.BashServiceLogsRequest) (output.Result, error) {
	if params.Stream == "" {
		params.Stream = "stdout"
	}
	svc, err := s.exec.ServiceStatus(ctx, params.Name)
	if err != nil {
		return output.Result{}, err
	}

	if params.Follow && svc.State == executor.ServiceStateRunning {
		idle := time.Duration(params.FollowIdleMS) * time.Millisecond
		if idle <= 0 {
			idle = 3 * time.Second
		}
		overall := time.Duration(params.FollowTimeoutMS) * time.Millisecond
		if overall <= 0 {
			overall = 30 * time.Second
		}
		maxLines := params.FollowMaxLines
		if maxLines <= 0 {
			maxLines = 10000
		}
		startLine := 0
		if params.Tail > 0 {
			if current, cerr := s.readOutput(ctx, agentrpc.BashOutputRequest{ID: svc.InvocationID, Stream: params.Stream}); cerr == nil {
				startLine = int(current.Lines) - params.Tail
				if startLine < 0 {
					startLine = 0
				}
			}
		}
		return s.followServiceLogs(ctx, svc.InvocationID, params.Stream, startLine, idle, overall, maxLines)
	}

	if params.Tail > 0 && params.Lines == "" {
		current, err := s.readOutput(ctx, agentrpc.BashOutputRequest{
			ID: svc.InvocationID, Stream: params.Stream, Grep: params.Grep, Context: params.Context,
		})
		if err != nil {
			return output.Result{}, err
		}
		lines := strings.Split(current.Text, "\n")
		if len(lines) > params.Tail {
			lines = lines[len(lines)-params.Tail:]
		}
		current.Text = strings.Join(lines, "\n")
		return current, nil
	}

	return s.readOutput(ctx, agentrpc.BashOutputRequest{
		ID: svc.InvocationID, Stream: params.Stream, Lines: params.Lines, Grep: params.Grep, Context: params.Context,
	})
}

// followServiceLogs polls a running invocation's live stdout/stderr for
// lines beyond startLine, returning what accumulated once idleTimeout
// passes with nothing new, maxLines is reached, overallTimeout expires, or
// the process itself stops. There's no push transport on this connection,
// so this blocks the RPC call rather than truly streaming.
func (s *Server) followServiceLogs(ctx context.Context, invocationID, streamName string, startLine int, idleTimeout, overallTimeout time.Duration, maxLines int) (output.Result, error) {
	deadline := time.Now().Add(overallTimeout)
	idleDeadline := time.Now().Add(idleTimeout)
	cursor := startLine
	var lines []string
	running := true

	for {
		reader, isRunning, err := s.exec.OpenRunning(invocationID, streamName)
		if err != nil {
			return output.Result{}, err
		}
		running = isRunning
		if reader != nil {
			result, rerr := output.Read(reader, output.Options{})
			reader.Close()
			if rerr != nil {
				return output.Result{}, rerr
			}
			if total := int(result.Lines); total > cursor {
				all := strings.Split(result.Text, "\n")
				if cursor < len(all) {
					lines = append(lines, all[cursor:]...)
				}
				cursor = total
				idleDeadline = time.Now().Add(idleTimeout)
				if len(lines) >= maxLines {
					lines = lines[:maxLines]
					break
				}
			}
		}
		if !running {
			break
		}
		if !time.Now().Before(deadline) || !time.Now().Before(idleDeadline) {
			break
		}
		select {
		case <-ctx.Done():
			return output.Result{}, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}

	text := strings.Join(lines, "\n")
	return output.Result{Text: text, Lines: int64(len(lines)), Bytes: int64(len(text)), Running: running}, nil
}

func (s *Server) readOutput(ctx context.Context, params agentrpc.BashOutputRequest) (output.Result, error) {
	if params.Stream == "" {
		params.Stream = "stdout"
	}
	if params.Mode == "templates" {
		analysis, err := s.readTemplates(ctx, agentrpc.BashTemplatesRequest{
			ID:       params.ID,
			Stream:   params.Stream,
			Baseline: true,
		})
		if err != nil {
			return output.Result{}, err
		}
		summaryJSON, _ := json.Marshal(analysis)
		return output.Result{
			Text:      string(summaryJSON),
			Bytes:     int64(len(summaryJSON)),
			Lines:     int64(len(analysis.Templates)),
			Truncated: false,
		}, nil
	}
	options := output.Options{Lines: params.Lines, Grep: params.Grep, Context: params.Context}
	reader, running, err := s.exec.OpenRunning(params.ID, params.Stream)
	if err != nil {
		return output.Result{}, err
	}
	if running {
		defer reader.Close()
		if options.Grep != "" && options.Lines == "" {
			if file, ok := reader.(*os.File); ok {
				result, err := output.Grep(file, options)
				result.Running = true
				return result, err
			}
		}
		result, err := output.Read(reader, options)
		result.Running = true
		return result, err
	}
	invocation, err := s.store.GetInvocation(ctx, params.ID)
	if err != nil {
		return output.Result{}, err
	}
	digest, err := digestForStream(invocation, params.Stream)
	if err != nil {
		return output.Result{}, err
	}
	file, err := s.blobs.Open(digest)
	if err != nil {
		return output.Result{}, err
	}
	defer file.Close()
	var idx *storage.LineIndex
	if options.Grep == "" && options.Lines != "" {
		idx, err = s.blobs.LoadIndex(digest)
		if err != nil {
			idx = nil
		}
		if idx == nil {
			idx, _ = s.blobs.Rebuild(digest)
		}
	}
	return output.ReadFile(file, idx, options)
}

// digestForStream picks the blob digest for an invocation's stdout or
// stderr stream — the same lookup was duplicated at every call site that
// needed to go from an invocation to its raw stream blob.
func digestForStream(invocation storage.Invocation, stream string) (string, error) {
	switch stream {
	case "stdout":
		return invocation.Stdout.SHA256, nil
	case "stderr":
		return invocation.Stderr.SHA256, nil
	default:
		return "", errors.New("stream must be stdout or stderr")
	}
}

func (s *Server) readTemplates(ctx context.Context, params agentrpc.BashTemplatesRequest) (*analyzer.LogAnalysis, error) {
	if params.Stream == "" {
		params.Stream = "stdout"
	}

	invocation, err := s.store.GetInvocation(ctx, params.ID)
	if err != nil {
		return nil, err
	}

	// 1. Check if templates are already stored in SQLite
	stored, err := s.store.GetLogTemplates(ctx, params.ID, params.Stream)
	var analysis *analyzer.LogAnalysis
	if err == nil && len(stored) > 0 {
		analysis = &analyzer.LogAnalysis{
			InvocationID: params.ID,
			Stream:       params.Stream,
			Levels:       make(map[string]int),
		}
		for _, st := range stored {
			analysis.Templates = append(analysis.Templates, analyzer.LogTemplate{
				ID:             st.TemplateID,
				Template:       st.Template,
				Count:          st.Count,
				FirstLine:      st.FirstLine,
				LastLine:       st.LastLine,
				ExemplarOffset: st.ExemplarOffset,
				Exemplar:       st.Exemplar,
				Level:          st.Level,
				IsStackTrace:   st.IsStackTrace,
			})
			analysis.TotalLines += st.Count
			if st.Level != "" {
				analysis.Levels[st.Level] += st.Count
			}
		}
	} else {
		// 2. Derive templates by reading the raw stream blob
		digest, err := digestForStream(invocation, params.Stream)
		if err != nil {
			return nil, err
		}

		file, err := s.blobs.Open(digest)
		if err != nil {
			return nil, err
		}
		defer file.Close()

		analysis = analyzer.AnalyzeReader(params.ID, params.Stream, file, analyzer.DefaultMaxAnalysisBytes)

		// Persist derived templates into SQLite
		var toStore []storage.StoredLogTemplate
		for _, t := range analysis.Templates {
			toStore = append(toStore, storage.StoredLogTemplate{
				InvocationID:   params.ID,
				Stream:         params.Stream,
				TemplateID:     t.ID,
				Template:       t.Template,
				Count:          t.Count,
				FirstLine:      t.FirstLine,
				LastLine:       t.LastLine,
				ExemplarOffset: t.ExemplarOffset,
				Exemplar:       t.Exemplar,
				Level:          t.Level,
				IsStackTrace:   t.IsStackTrace,
			})
		}
		_ = s.store.SaveLogTemplates(ctx, params.ID, params.Stream, toStore)
	}

	// 3. Baseline comparison if requested
	if params.Baseline {
		priorCounts, priorRunCount, err := s.store.GetPriorTemplatesForCommand(ctx, invocation.Command, invocation.ID)
		if err == nil && priorRunCount > 0 {
			analyzer.BaselineComparison(analysis, priorCounts, priorRunCount)
		}
	}

	return analysis, nil
}

func (s *Server) Shutdown() {
	s.once.Do(func() {
		close(s.stop)
	})
}

func (s *Server) cleanup() {
	_ = os.Remove(s.paths.Socket)
	_ = os.Remove(s.paths.PID)
}

func readinessSpec(r *agentrpc.ReadinessSpec) service.ReadinessSpec {
	if r == nil {
		return service.ReadinessSpec{}
	}
	return service.ReadinessSpec{
		Port: r.Port, Host: r.Host, StdoutRegex: r.StdoutRegex, TailBytes: r.TailBytes,
		HTTPURL:      r.HTTPURL,
		Timeout:      time.Duration(r.TimeoutMS) * time.Millisecond,
		PollInterval: time.Duration(r.PollIntervalMS) * time.Millisecond,
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
