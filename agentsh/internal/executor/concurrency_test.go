//go:build unix

package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/storage"
)

// Processes and OpenRunning read invocation state while the waiter mutates it.
// Run under -race: the timeout path used to write State without the lock.
func TestConcurrentExecuteInspectAndRead(t *testing.T) {
	exec, _, root := testExecutor(t)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// The inspector gets its own WaitGroup: it only stops once the commands are
	// done, so waiting on it together with them would deadlock.
	var inspector sync.WaitGroup
	inspector.Add(1)
	go func() {
		defer inspector.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, invocation := range exec.Processes("") {
				reader, _, err := exec.OpenRunning(invocation.ID, "stdout")
				if err == nil && reader != nil {
					_ = reader.Close()
				}
			}
			time.Sleep(time.Millisecond)
		}
	}()

	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			timeout := 0 * time.Second
			command := "echo out; echo err >&2"
			if n%3 == 0 {
				// Exercise the timeout path, which writes State from the waiter.
				command, timeout = "sleep 5", 80*time.Millisecond
			}
			_, err := exec.Execute(context.Background(), Request{
				Command: command, CWD: root, Timeout: timeout,
			})
			if err != nil {
				t.Errorf("execute: %v", err)
			}
		}(i)
	}

	wg.Wait()
	close(stop)
	inspector.Wait()
}

// Kill races the waiter for the same invocation record.
func TestConcurrentKillAndCompletion(t *testing.T) {
	exec, _, root := testExecutor(t)
	for i := 0; i < 5; i++ {
		var wg sync.WaitGroup
		wg.Add(1)
		var invocationID string
		go func() {
			defer wg.Done()
			invocation, err := exec.Execute(context.Background(), Request{
				Command: "sleep 0.2", CWD: root, Background: true,
			})
			if err != nil {
				t.Errorf("execute: %v", err)
				return
			}
			invocationID = invocation.ID
		}()
		wg.Wait()

		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = exec.Kill(invocationID, 0)
		}()
		go func() {
			defer wg.Done()
			_ = exec.Processes("")
		}()
		wg.Wait()
	}
}

// A failed start must not leave its temp files behind. Each abandoned writer
// also holds an open descriptor.
func TestFailedStartLeavesNoStreamTempFiles(t *testing.T) {
	exec, _, root := testExecutor(t)
	blobRoot := exec.Blobs.Root

	for i := 0; i < 5; i++ {
		// A working directory that does not exist fails the start after both
		// blob writers have been created.
		_, err := exec.Execute(context.Background(), Request{
			Command: "echo hi", CWD: filepath.Join(root, "definitely-not-here"),
		})
		if err == nil {
			t.Fatal("expected the start to fail for a missing working directory")
		}
	}

	entries, err := os.ReadDir(blobRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stream-") {
			t.Errorf("failed start left a temp blob behind: %s", entry.Name())
		}
	}
}

// A supervisor failure must not be reported as the command exiting 1.
func TestSupervisorFailureIsNotReportedAsCommandExit(t *testing.T) {
	exec, _, root := testExecutor(t)
	invocation, err := exec.Execute(context.Background(), Request{
		Command: "echo hi", CWD: filepath.Join(root, "missing"),
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if invocation.ExitCode != nil && *invocation.ExitCode == 1 {
		t.Error("supervisor failure reported as exit code 1, which the command never produced")
	}
}

// The idle check must measure silence, not elapsed runtime: a command that
// keeps printing is not waiting on input.
func TestChattyCommandIsNotReportedWaitingOnInput(t *testing.T) {
	exec, _, root := testExecutor(t)
	invocation, err := exec.Execute(context.Background(), Request{
		Command:     "for i in 1 2 3 4 5 6; do echo tick; sleep 0.05; done",
		CWD:         root,
		Interactive: true,
		IdleTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if invocation.State == storage.StateWaitingOnInput {
		t.Error("a command producing output continuously was reported as waiting on input")
	}
}
