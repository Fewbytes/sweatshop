//go:build linux

package executor

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func isWaitingOnStdin(pid int) bool {
	if pid <= 0 {
		return false
	}
	// On Linux, inspect wchan for read/wait syscalls or wait state
	wchan, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "wchan"))
	if err == nil {
		str := strings.ToLower(strings.TrimSpace(string(wchan)))
		if strings.Contains(str, "read") || strings.Contains(str, "poll") || strings.Contains(str, "select") || strings.Contains(str, "wait") {
			return true
		}
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err == nil {
		fields := strings.Fields(string(stat))
		if len(fields) > 2 {
			// S is interruptible sleep (waiting on an event/IO)
			if fields[2] == "S" {
				return true
			}
		}
	}
	return true // Fallback to idle-based detection
}
