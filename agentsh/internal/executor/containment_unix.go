//go:build unix

package executor

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type ProcessGroup struct{}

func (ProcessGroup) Configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func (ProcessGroup) Signal(process *os.Process, signal syscall.Signal) error {
	err := syscall.Kill(-process.Pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
