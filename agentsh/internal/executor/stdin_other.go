//go:build !linux

package executor

func isWaitingOnStdin(pid int) bool {
	return true // Fallback to output-idle heuristic where platform-specific probe is unavailable
}
