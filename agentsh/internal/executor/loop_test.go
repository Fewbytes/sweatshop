package executor

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/storage"
)

func TestLoopDetectorTriggersOnThreshold(t *testing.T) {
	detector := NewLoopDetector(LoopConfig{
		Threshold: 3,
		Window:    5 * time.Minute,
	})

	inv1 := storage.Invocation{ID: "inv_11111111", Session: "s1", Command: "go test ./..."}
	inv2 := storage.Invocation{ID: "inv_22222222", Session: "s1", Command: "go test  ./... "}
	inv3 := storage.Invocation{ID: "inv_33333333", Session: "s1", Command: "go test ./...;"}

	// 1st failure: no warning
	w1 := detector.RecordAndCheck(inv1)
	if w1 != "" {
		t.Fatalf("expected no warning on 1st failure, got: %q", w1)
	}

	// 2nd failure: no warning
	w2 := detector.RecordAndCheck(inv2)
	if w2 != "" {
		t.Fatalf("expected no warning on 2nd failure, got: %q", w2)
	}

	// 3rd equivalent failure: warning triggered with prior IDs
	w3 := detector.RecordAndCheck(inv3)
	if w3 == "" {
		t.Fatal("expected warning on 3rd failure, got empty")
	}
	if !strings.Contains(w3, "loop detected") {
		t.Errorf("warning missing 'loop detected': %q", w3)
	}
	if !strings.Contains(w3, "3 times") {
		t.Errorf("warning missing '3 times': %q", w3)
	}
	if !strings.Contains(w3, "inv_11111111") || !strings.Contains(w3, "inv_22222222") {
		t.Errorf("warning missing prior IDs: %q", w3)
	}
}

func TestLoopDetectorDistinctCommandsDoNotTrigger(t *testing.T) {
	detector := NewLoopDetector(LoopConfig{
		Threshold: 3,
		Window:    5 * time.Minute,
	})

	commands := []string{
		"go test ./pkg1",
		"go test ./pkg2",
		"go test ./pkg3",
		"go test ./pkg4",
	}

	for i, cmd := range commands {
		inv := storage.Invocation{
			ID:      string(rune('a' + i)),
			Session: "default",
			Command: cmd,
		}
		w := detector.RecordAndCheck(inv)
		if w != "" {
			t.Errorf("distinct command %q unexpectedly triggered warning: %q", cmd, w)
		}
	}
}

func TestLoopDetectorSuccessResetsTracking(t *testing.T) {
	detector := NewLoopDetector(LoopConfig{
		Threshold: 3,
		Window:    5 * time.Minute,
	})

	invFail1 := storage.Invocation{ID: "inv_f1", Session: "s1", Command: "pytest tests/"}
	invFail2 := storage.Invocation{ID: "inv_f2", Session: "s1", Command: "pytest tests/"}
	invSuccess := storage.Invocation{ID: "inv_s1", Session: "s1", Command: "pytest tests/"}
	invFail3 := storage.Invocation{ID: "inv_f3", Session: "s1", Command: "pytest tests/"}

	detector.RecordAndCheck(invFail1)
	detector.RecordAndCheck(invFail2)

	// Success clears prior failures
	detector.RecordSuccess(invSuccess)

	// 3rd total failure (but 1st after success) should not trigger
	w := detector.RecordAndCheck(invFail3)
	if w != "" {
		t.Errorf("expected reset after success, but got warning: %q", w)
	}
}

func TestLoopDetectorWindowExpiry(t *testing.T) {
	detector := NewLoopDetector(LoopConfig{
		Threshold: 2,
		Window:    50 * time.Millisecond,
	})

	past := time.Now().UTC().Add(-100 * time.Millisecond)
	inv1 := storage.Invocation{
		ID:      "inv_old",
		Session: "s1",
		Command: "cargo build",
		EndedAt: &past,
	}
	detector.RecordAndCheck(inv1)

	// Current failure happens well after the 50ms window
	inv2 := storage.Invocation{
		ID:      "inv_new",
		Session: "s1",
		Command: "cargo build",
	}
	w := detector.RecordAndCheck(inv2)
	if w != "" {
		t.Errorf("expired failure in window unexpectedly triggered: %q", w)
	}
}

func TestLoopDetectorConcurrentSessions(t *testing.T) {
	detector := NewLoopDetector(LoopConfig{
		Threshold: 3,
		Window:    5 * time.Minute,
	})

	var wg sync.WaitGroup
	sessionCount := 10

	for s := 0; s < sessionCount; s++ {
		wg.Add(1)
		sessionName := string(rune('A' + s))
		go func(sess string) {
			defer wg.Done()
			for i := 1; i <= 2; i++ {
				inv := storage.Invocation{
					ID:      string(rune('0' + i)),
					Session: sess,
					Command: "make build",
				}
				w := detector.RecordAndCheck(inv)
				// 2 failures per session with threshold 3 should never trigger
				if w != "" {
					t.Errorf("session %s unexpectedly triggered on count %d: %q", sess, i, w)
				}
			}
		}(sessionName)
	}
	wg.Wait()
}

func TestExecuteAppendsLoopWarning(t *testing.T) {
	exec, _, root := testExecutor(t)
	exec.LoopDetector = NewLoopDetector(LoopConfig{
		Threshold: 2,
		Window:    time.Minute,
	})

	cmd := "false"

	// 1st failure: normal recovery hint, no loop warning
	inv1, err := exec.Execute(context.Background(), Request{Command: cmd, CWD: root, Session: "test-loop"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(inv1.Stdout.Preview, "loop detected") {
		t.Fatalf("1st failure had loop warning: %s", inv1.Stdout.Preview)
	}

	// 2nd failure: threshold=2 -> loop warning appended
	inv2, err := exec.Execute(context.Background(), Request{Command: cmd, CWD: root, Session: "test-loop"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inv2.Stdout.Preview, "loop detected") {
		t.Fatalf("2nd failure missing loop warning: %s", inv2.Stdout.Preview)
	}
	if !strings.Contains(inv2.Stdout.Preview, inv1.ID) {
		t.Fatalf("loop warning missing prior ID %s: %s", inv1.ID, inv2.Stdout.Preview)
	}
}
