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

// View is the invocation projection sent to clients.
//
// It deliberately omits EnvAfter, Argv, StdinSHA256 and PathsTouched. A full
// environment snapshot on every response costs an agent more context than the
// command's own output, and echoes the daemon's environment — credentials
// included — back into the transcript. EnvDelta carries what actually changed;
// the complete record stays in the database for BashState and the exporter.
type View struct {
	ID         string            `json:"id"`
	Session    string            `json:"session"`
	Command    string            `json:"command"`
	State      InvocationState   `json:"state"`
	PID        *int              `json:"pid,omitempty"`
	ExitCode   *int              `json:"exit_code,omitempty"`
	Reason     *string           `json:"reason,omitempty"`
	Signal     *int              `json:"signal,omitempty"`
	StartedAt  time.Time         `json:"started_at"`
	EndedAt    *time.Time        `json:"ended_at,omitempty"`
	DurationMS int64             `json:"duration_ms"`
	CWD        string            `json:"cwd"`
	CWDAfter   *string           `json:"cwd_after,omitempty"`
	EnvDelta   map[string]string `json:"env_delta,omitempty"`
	Stdout     StreamRef         `json:"stdout"`
	Stderr     StreamRef         `json:"stderr"`
	Summary    *CommandSummary   `json:"summary,omitempty"`
}

func (i Invocation) View() View {
	return View{
		ID: i.ID, Session: i.Session, Command: i.Command, State: i.State, PID: i.PID,
		ExitCode: i.ExitCode, Reason: i.Reason, Signal: i.Signal,
		StartedAt: i.StartedAt, EndedAt: i.EndedAt, DurationMS: i.DurationMS,
		CWD: i.CWD, CWDAfter: i.CWDAfter, EnvDelta: i.EnvDelta,
		Stdout: i.Stdout, Stderr: i.Stderr, Summary: i.Summary,
	}
}

func Views(invocations []Invocation) []View {
	views := make([]View, 0, len(invocations))
	for _, invocation := range invocations {
		views = append(views, invocation.View())
	}
	return views
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
