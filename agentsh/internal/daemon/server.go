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
	"sync"
	"syscall"
	"time"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/executor"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/output"
	agentrpc "github.com/avishai-ish-shalom/sweatshop/agentsh/internal/rpc"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/storage"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/workspace"
)

type Server struct {
	paths workspace.Paths
	ln    net.Listener
	store *storage.Store
	exec  *executor.Executor
	stop  chan struct{}
	once  sync.Once
	wg    sync.WaitGroup
}

func New(paths workspace.Paths) *Server {
	return &Server{paths: paths, stop: make(chan struct{})}
}

func (s *Server) Serve(ctx context.Context) error {
	if err := s.paths.Ensure(); err != nil {
		return err
	}
	store, err := storage.Open(ctx, storage.Config{
		Path: s.paths.Database, RemoteURL: os.Getenv("TURSO_DATABASE_URL"),
		AuthToken: os.Getenv("TURSO_AUTH_TOKEN"), SyncInterval: time.Minute,
	})
	if err != nil {
		return fmt.Errorf("open invocation storage: %w", err)
	}
	s.store = store
	s.exec = executor.New(store, storage.BlobStore{Root: s.paths.Blobs})
	defer store.Close()
	if _, err := store.Reconcile(ctx, processAlive); err != nil {
		return fmt.Errorf("reconcile invocations: %w", err)
	}
	if err := s.listen(); err != nil {
		return err
	}
	defer s.cleanup()

	sigCtx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	go func() {
		select {
		case <-sigCtx.Done():
			s.Shutdown()
		case <-s.stop:
		}
	}()

	for {
		conn, err := s.ln.Accept()
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
			s.handle(conn)
		}()
	}
}

func (s *Server) listen() error {
	ln, err := net.Listen("unix", s.paths.Socket)
	if err != nil && errors.Is(err, syscall.EADDRINUSE) {
		conn, dialErr := net.Dial("unix", s.paths.Socket)
		if dialErr == nil {
			conn.Close()
			return fmt.Errorf("daemon already running at %s", s.paths.Socket)
		}
		if removeErr := os.Remove(s.paths.Socket); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
		ln, err = net.Listen("unix", s.paths.Socket)
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(s.paths.Socket, 0o600); err != nil {
		ln.Close()
		return err
	}
	s.ln = ln
	return os.WriteFile(s.paths.PID, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
}

func (s *Server) handle(conn net.Conn) {
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

	var response agentrpc.Response
	switch request.Op {
	case agentrpc.OpHealth:
		response = agentrpc.Success(request.ID, agentrpc.Health{Status: "ok", PID: os.Getpid(), Workspace: s.paths.Root})
	case agentrpc.OpShutdown:
		response = agentrpc.Success(request.ID, map[string]string{"status": "stopping"})
		defer s.Shutdown()
	case agentrpc.OpBash:
		var params agentrpc.BashRequest
		if err := json.Unmarshal(request.Params, &params); err != nil {
			response = agentrpc.Failure(request.ID, "invalid_params", err.Error())
			break
		}
		invocation, err := s.exec.Execute(context.Background(), executor.Request{
			Command: params.Command, Session: params.Session, CWD: s.paths.Root,
			Timeout:     time.Duration(params.TimeoutMS) * time.Millisecond,
			IdleTimeout: time.Duration(params.IdleWaitMS) * time.Millisecond,
			Background:  params.Background,
			Interactive: params.Interactive,
			Stdin:       params.Stdin,
		})
		if err != nil {
			response = agentrpc.Failure(request.ID, "execution", err.Error())
		} else {
			response = agentrpc.Success(request.ID, invocation)
		}
	case agentrpc.OpBashOutput:
		var params agentrpc.BashOutputRequest
		if err := json.Unmarshal(request.Params, &params); err != nil {
			response = agentrpc.Failure(request.ID, "invalid_params", err.Error())
			break
		}
		result, err := s.readOutput(params)
		if err != nil {
			response = agentrpc.Failure(request.ID, "output", err.Error())
		} else {
			response = agentrpc.Success(request.ID, result)
		}
	case agentrpc.OpBashInput:
		var params agentrpc.BashInputRequest
		if err := json.Unmarshal(request.Params, &params); err != nil {
			response = agentrpc.Failure(request.ID, "invalid_params", err.Error())
			break
		}
		if err := s.exec.WriteInput(params.ID, []byte(params.Data)); err != nil {
			response = agentrpc.Failure(request.ID, "input", err.Error())
		} else {
			response = agentrpc.Success(request.ID, map[string]string{"status": "written"})
		}
	case agentrpc.OpBashKill:
		var params agentrpc.BashKillRequest
		if err := json.Unmarshal(request.Params, &params); err != nil {
			response = agentrpc.Failure(request.ID, "invalid_params", err.Error())
			break
		}
		sig := syscall.Signal(params.Signal)
		if sig == 0 {
			sig = syscall.SIGTERM
		}
		if err := s.exec.Kill(params.ID, sig); err != nil {
			response = agentrpc.Failure(request.ID, "kill", err.Error())
		} else {
			response = agentrpc.Success(request.ID, map[string]string{"status": "killed"})
		}
	case agentrpc.OpBashState:
		var params agentrpc.BashStateRequest
		if len(request.Params) > 0 {
			_ = json.Unmarshal(request.Params, &params)
		}
		if params.Session == "" {
			params.Session = "default"
		}
		state := s.exec.Sessions.Get(params.Session, s.paths.Root)
		response = agentrpc.Success(request.ID, state)
	case agentrpc.OpBashProcesses:
		var params agentrpc.BashStateRequest
		_ = json.Unmarshal(request.Params, &params)
		response = agentrpc.Success(request.ID, s.exec.Processes(params.Session))
	case agentrpc.OpBashHistory:
		var params agentrpc.BashHistoryRequest
		if err := json.Unmarshal(request.Params, &params); err != nil {
			response = agentrpc.Failure(request.ID, "invalid_params", err.Error())
			break
		}
		history, err := s.store.History(context.Background(), params.Session, params.Command, params.Since, params.Exit, params.Limit)
		if err != nil {
			response = agentrpc.Failure(request.ID, "history", err.Error())
		} else {
			response = agentrpc.Success(request.ID, history)
		}
	case agentrpc.OpBashReplay:
		var params agentrpc.BashReplayRequest
		if err := json.Unmarshal(request.Params, &params); err != nil {
			response = agentrpc.Failure(request.ID, "invalid_params", err.Error())
			break
		}
		original, err := s.store.GetInvocation(context.Background(), params.ID)
		if err != nil {
			response = agentrpc.Failure(request.ID, "replay", err.Error())
			break
		}
		replayed, err := s.exec.Replay(context.Background(), original)
		if err != nil {
			response = agentrpc.Failure(request.ID, "replay", err.Error())
		} else {
			response = agentrpc.Success(request.ID, replayed)
		}
	case agentrpc.OpBashGC:
		var params agentrpc.BashGCRequest
		_ = json.Unmarshal(request.Params, &params)
		gc, err := (storage.BlobStore{Root: s.paths.Blobs}).GC(time.Duration(params.OlderThanHours)*time.Hour, params.MaxBytes)
		if err != nil {
			response = agentrpc.Failure(request.ID, "gc", err.Error())
		} else {
			response = agentrpc.Success(request.ID, gc)
		}
	default:
		response = agentrpc.Failure(request.ID, "unknown_operation", request.Op)
	}
	_ = json.NewEncoder(conn).Encode(response)
}

func (s *Server) readOutput(params agentrpc.BashOutputRequest) (output.Result, error) {
	if params.Stream == "" {
		params.Stream = "stdout"
	}
	reader, running, err := s.exec.OpenRunning(params.ID, params.Stream)
	if err != nil {
		return output.Result{}, err
	}
	if !running {
		invocation, err := s.store.GetInvocation(context.Background(), params.ID)
		if err != nil {
			return output.Result{}, err
		}
		var digest string
		switch params.Stream {
		case "stdout":
			digest = invocation.Stdout.SHA256
		case "stderr":
			digest = invocation.Stderr.SHA256
		default:
			return output.Result{}, errors.New("stream must be stdout or stderr")
		}
		reader, err = storage.BlobStore{Root: s.paths.Blobs}.Open(digest)
		if err != nil {
			return output.Result{}, err
		}
	}
	defer reader.Close()
	result, err := output.Read(reader, output.Options{Lines: params.Lines, Grep: params.Grep, Context: params.Context})
	result.Running = running
	return result, err
}

func (s *Server) Shutdown() {
	s.once.Do(func() {
		close(s.stop)
		if s.ln != nil {
			_ = s.ln.Close()
		}
	})
}

func (s *Server) cleanup() {
	_ = os.Remove(s.paths.Socket)
	_ = os.Remove(s.paths.PID)
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
