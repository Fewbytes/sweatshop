//go:build !unix

package executor

import (
	"os"
	"os/exec"
	"syscall"
)

type ProcessGroup struct{ Null }

func (ProcessGroup) Configure(*exec.Cmd) {}
func (ProcessGroup) Signal(process *os.Process, signal syscall.Signal) error {
	return process.Signal(signal)
}
