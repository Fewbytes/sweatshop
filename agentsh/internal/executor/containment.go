package executor

import (
	"os"
	"os/exec"
	"syscall"
)

type Containment interface {
	Configure(*exec.Cmd)
	Signal(*os.Process, syscall.Signal) error
}

// ProcessLifecycle is implemented by containment backends that need to attach
// a newly started PID and inspect/clean invocation-specific state after exit.
type ProcessPreparer interface {
	Prepare(invocationID string, cmd *exec.Cmd) error
}

type CommandPrelude interface {
	Prelude(invocationID string) string
}

type ProcessLifecycle interface {
	Started(invocationID string, process *os.Process) error
	OOMKilled(invocationID string) bool
	Cleanup(invocationID string) error
}

type WholeTreeKiller interface {
	KillInvocation(invocationID string) error
}

type Null struct{}

func (Null) Configure(*exec.Cmd) {}
func (Null) Signal(process *os.Process, signal syscall.Signal) error {
	return process.Signal(signal)
}
