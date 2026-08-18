package rpc

import (
	"encoding/json"
	"fmt"
)

const Version = 1

const (
	OpHealth        = "health"
	OpShutdown      = "shutdown"
	OpBash          = "bash"
	OpBashOutput    = "bash_output"
	OpBashInput     = "bash_input"
	OpBashKill      = "bash_kill"
	OpBashState     = "bash_state"
	OpBashHistory   = "bash_history"
	OpBashReplay    = "bash_replay"
	OpBashProcesses = "bash_processes"
	OpBashGC        = "bash_gc"
	OpBashTemplates = "bash_templates"

	OpBashService       = "bash_service"
	OpBashServiceStatus = "bash_service_status"
	OpBashServiceKill   = "bash_service_kill"
)

type Request struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	Op      string          `json:"op"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

type Health struct {
	Status    string `json:"status"`
	PID       int    `json:"pid"`
	Workspace string `json:"workspace"`
}

type BashRequest struct {
	Command     string `json:"command"`
	Session     string `json:"session,omitempty"`
	TimeoutMS   int64  `json:"timeout_ms,omitempty"`
	IdleWaitMS  int64  `json:"idle_wait_ms,omitempty"`
	Background  bool   `json:"background,omitempty"`
	Interactive bool   `json:"interactive,omitempty"`
	Stdin       string `json:"stdin,omitempty"`
}

type BashOutputRequest struct {
	ID      string `json:"id"`
	Stream  string `json:"stream,omitempty"`
	Lines   string `json:"lines,omitempty"`
	Grep    string `json:"grep,omitempty"`
	Context int    `json:"context,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

type BashTemplatesRequest struct {
	ID       string `json:"id"`
	Stream   string `json:"stream,omitempty"`
	Baseline bool   `json:"baseline,omitempty"`
}

type BashInputRequest struct {
	ID   string `json:"id"`
	Data string `json:"data"`
}

type BashKillRequest struct {
	ID     string `json:"id"`
	Signal int    `json:"signal,omitempty"`
}

type BashStateRequest struct {
	Session string `json:"session,omitempty"`
}

type BashHistoryRequest struct {
	Session string `json:"session,omitempty"`
	Command string `json:"command,omitempty"`
	Exit    *int   `json:"exit,omitempty"`
	Since   string `json:"since,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type BashReplayRequest struct {
	ID string `json:"id"`
}

type BashServiceRequest struct {
	Name      string `json:"name"`
	Command   string `json:"command"`
	Session   string `json:"session,omitempty"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`

	Readiness *ReadinessSpec `json:"readiness,omitempty"`
}

// ReadinessSpec configures BashService to block until a service is ready
// instead of returning as soon as it's started. Fields left unset skip that
// predicate; leaving them all unset makes readiness instant.
type ReadinessSpec struct {
	Port        int    `json:"port,omitempty"`
	Host        string `json:"host,omitempty"`
	StdoutRegex string `json:"stdout_regex,omitempty"`
	TailBytes   int    `json:"tail_bytes,omitempty"`
	HTTPURL     string `json:"http_url,omitempty"`

	TimeoutMS      int64 `json:"timeout_ms,omitempty"`
	PollIntervalMS int64 `json:"poll_interval_ms,omitempty"`
}

type BashServiceStatusRequest struct {
	Name string `json:"name"`
}

type BashServiceKillRequest struct {
	Name   string `json:"name"`
	Signal int    `json:"signal,omitempty"`
}

type BashGCRequest struct {
	OlderThanHours int   `json:"older_than_hours,omitempty"`
	MaxBytes       int64 `json:"max_bytes,omitempty"`
}

func Success(id string, value any) Response {
	data, err := json.Marshal(value)
	if err != nil {
		return Failure(id, "internal", err.Error())
	}
	return Response{Version: Version, ID: id, Result: data}
}

func Failure(id, code, message string) Response {
	return Response{Version: Version, ID: id, Error: &Error{Code: code, Message: message}}
}
