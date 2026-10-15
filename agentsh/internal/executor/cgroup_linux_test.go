//go:build linux

package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCgroupKillsSetsidDescendant(t *testing.T) {
	cgroup, err := DetectCgroup()
	if err != nil {
		t.Skip(err)
	}
	id := "inv_cgtest"
	cmd := exec.Command("sh", "-c", cgroup.Prelude(id)+`setsid sh -c 'echo $$ > escaped.pid; exec sleep 30' & wait`)
	cmd.Dir = t.TempDir()
	cgroup.Configure(cmd)
	if err := cgroup.Prepare(id, cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cgroup.Started(id, cmd.Process); err != nil {
		t.Fatal(err)
	}
	defer cgroup.Cleanup(id)
	pidFile := filepath.Join(cmd.Dir, "escaped.pid")
	var child int
	pollUntil(t, 2*time.Second, "escaped child to write its pid", func() bool {
		data, readErr := os.ReadFile(pidFile)
		if readErr != nil {
			return false
		}
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return child != 0
	})
	if child == 0 {
		t.Fatal("escaped child did not start")
	}
	if err := cgroup.KillInvocation(id); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	pollUntil(t, time.Second, fmt.Sprintf("cgroup to depopulate after killing setsid descendant %d", child), func() bool {
		events, err := os.ReadFile(filepath.Join(cgroup.path(id), "cgroup.events"))
		return err == nil && strings.Contains(string(events), "populated 0")
	})
}

func TestCgroupReadsOOMEvent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agentsh-inv_oom")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "memory.events"), []byte("low 0\noom 1\noom_kill 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !(&Cgroup{Root: root}).OOMKilled("inv_oom") {
		t.Fatal("oom_kill event not detected")
	}
}
