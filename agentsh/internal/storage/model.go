package storage

import "time"

type InvocationState string

const (
	StateRunning        InvocationState = "running"
	StateExited         InvocationState = "exited"
	StateTimeout        InvocationState = "timeout"
	StateKilled         InvocationState = "killed"
	StateWaitingOnInput InvocationState = "waiting_on_input"
	StateDaemonLost     InvocationState = "daemon_lost"
)

type StreamRef struct {
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes"`
	Lines     int64  `json:"lines"`
	Preview   string `json:"preview"`
	Truncated bool   `json:"truncated"`
}

type Invocation struct {
	ID           string
	Session      string
	PID          *int
	Argv         []string
	Command      string
	CWD          string
	EnvDelta     map[string]string
	StdinSHA256  *string
	State        InvocationState
	ExitCode     *int
	Reason       *string
	Signal       *int
	StartedAt    time.Time
	EndedAt      *time.Time
	DurationMS   int64
	Stdout       StreamRef
	Stderr       StreamRef
	CWDAfter     *string
	EnvAfter     map[string]string
	PathsTouched []string
}
