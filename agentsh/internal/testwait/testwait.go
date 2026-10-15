// Package testwait is the one place tests wait on wall-clock time.
package testwait

import (
	"testing"
	"time"
)

const pollInterval = 5 * time.Millisecond

// Until re-evaluates condition every few milliseconds until it holds and fails
// the test with a message naming what after timeout. Tests wait on observable
// state through it instead of sleeping a guessed duration.
func Until(t testing.TB, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-deadline.C:
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		case <-ticker.C:
		}
	}
}
