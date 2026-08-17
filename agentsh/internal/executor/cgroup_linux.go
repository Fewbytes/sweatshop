//go:build linux

package executor

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

type Cgroup struct {
	Root    string
	mu      sync.Mutex
	pending map[string]*os.File
}

func DetectCgroup() (*Cgroup, error) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return nil, err
	}
	var relative string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "0::") {
			relative = strings.TrimPrefix(line, "0::")
			break
		}
	}
	if relative == "" {
		return nil, errors.New("process is not in a cgroup v2 hierarchy")
	}
	root := filepath.Join("/sys/fs/cgroup", filepath.Clean("/"+relative))
	if _, err := os.Stat(filepath.Join(root, "cgroup.controllers")); err != nil {
		return nil, fmt.Errorf("cgroup v2 unavailable: %w", err)
	}
	probe, err := os.MkdirTemp(root, ".agentsh-probe-")
	if err != nil {
		return nil, fmt.Errorf("cgroup delegation unavailable at %s: %w", root, err)
	}
	_ = os.Remove(probe)
	return &Cgroup{Root: root, pending: make(map[string]*os.File)}, nil
}

// Configure uses a process group too, so attachment failures still leave a
// killable process tree.
func (c *Cgroup) Configure(cmd *exec.Cmd) { ProcessGroup{}.Configure(cmd) }

func (c *Cgroup) Prelude(id string) string {
	return fmt.Sprintf("printf '%%d' $$ > %q || exit 126\n", filepath.Join(c.path(id), "cgroup.procs"))
}

func (c *Cgroup) Prepare(id string, cmd *exec.Cmd) error {
	path := c.path(id)
	if err := os.Mkdir(path, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	// Keep the directory descriptor open through cmd.Start; CgroupFD is consumed
	// by clone3 and is not copied into the child.
	c.mu.Lock()
	if c.pending == nil {
		c.pending = make(map[string]*os.File)
	}
	c.pending[id] = file
	c.mu.Unlock()
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = int(file.Fd())
	return nil
}

func (c *Cgroup) Started(id string, _ *os.Process) error {
	c.mu.Lock()
	file := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if file != nil {
		return file.Close()
	}
	return nil
}

func (c *Cgroup) Signal(process *os.Process, signal syscall.Signal) error {
	if signal == syscall.SIGKILL {
		// cgroup.kill is used by KillInvocation, where the invocation ID is known.
		return syscall.Kill(-process.Pid, signal)
	}
	return syscall.Kill(-process.Pid, signal)
}

func (c *Cgroup) KillInvocation(id string) error {
	err := os.WriteFile(filepath.Join(c.path(id), "cgroup.kill"), []byte("1"), 0o200)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (c *Cgroup) OOMKilled(id string) bool {
	file, err := os.Open(filepath.Join(c.path(id), "memory.events"))
	if err != nil {
		return false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "oom_kill" {
			count, _ := strconv.ParseUint(fields[1], 10, 64)
			return count > 0
		}
	}
	return false
}

func (c *Cgroup) Cleanup(id string) error {
	err := os.Remove(c.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (c *Cgroup) path(id string) string { return filepath.Join(c.Root, "agentsh-"+id) }
