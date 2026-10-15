package testwait

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// recordingT captures Fatalf and stops the calling goroutine like testing.T.
type recordingT struct {
	testing.TB
	message string
}

func (r *recordingT) Helper() {}

func (r *recordingT) Fatalf(format string, args ...any) {
	r.message = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func TestUntilReturnsOnceConditionHolds(t *testing.T) {
	calls := 0
	Until(t, time.Minute, "third call", func() bool {
		calls++
		return calls == 3
	})
	if calls != 3 {
		t.Fatalf("condition evaluated %d times, want 3", calls)
	}
}

func TestUntilFailsNamingWhatTimedOut(t *testing.T) {
	rec := &recordingT{}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		Until(rec, 20*time.Millisecond, "the moon", func() bool { return false })
	}()
	<-finished
	if want := "timed out after 20ms waiting for the moon"; rec.message != want {
		t.Fatalf("message = %q, want %q", rec.message, want)
	}
}
