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

type Failure struct {
	Name     string `json:"name,omitempty"`
	Message  string `json:"message,omitempty"`
	Location string `json:"location,omitempty"`
	Excerpt  string `json:"excerpt,omitempty"`
}

type CommandSummary struct {
	Family    string    `json:"family"`
	Status    string    `json:"status"`
	Passed    int       `json:"passed,omitempty"`
	Failed    int       `json:"failed,omitempty"`
	Skipped   int       `json:"skipped,omitempty"`
	Total     int       `json:"total,omitempty"`
	Duration  string    `json:"duration,omitempty"`
	Added     int       `json:"added,omitempty"`
	Changed   int       `json:"changed,omitempty"`
	Destroyed int       `json:"destroyed,omitempty"`
	Failures  []Failure `json:"failures,omitempty"`
	Details   string    `json:"details,omitempty"`
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
	Summary      *CommandSummary `json:"summary,omitempty"`
}
