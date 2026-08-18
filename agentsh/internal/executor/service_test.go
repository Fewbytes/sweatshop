//go:build unix

package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// waitForNoProcesses blocks until the executor reports no running
// invocations, so a killed process's async blob-finalize goroutine can't
// race t.TempDir()'s cleanup after the test returns.
func waitForNoProcesses(t *testing.T, exec *Executor) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(exec.Processes("")) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("processes still running after deadline")
}

func TestStartServiceReturnsRunningRecordWithInvocationID(t *testing.T) {
	exec, _, root := testExecutor(t)
	svc, err := exec.StartService(context.Background(), ServiceRequest{
		Name: "web", Command: "sleep 30", CWD: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if svc.InvocationID == "" {
		t.Fatal("expected non-empty invocation id")
	}
	if svc.State != ServiceStateRunning {
		t.Fatalf("state = %q, want running", svc.State)
	}
	if svc.PID == 0 {
		t.Fatal("expected non-zero PID for a running service")
	}
	if svc.StartedAt.IsZero() {
		t.Fatal("expected StartedAt to be set")
	}
	_ = exec.KillService("web", syscall.SIGKILL)
	waitForNoProcesses(t, exec)
}

func TestStartServiceRejectsDuplicateNameWhileRunning(t *testing.T) {
	exec, _, root := testExecutor(t)
	if _, err := exec.StartService(context.Background(), ServiceRequest{Name: "web", Command: "sleep 30", CWD: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.StartService(context.Background(), ServiceRequest{Name: "web", Command: "sleep 30", CWD: root}); err == nil {
		t.Fatal("expected error starting a second service under a running name")
	}
	_ = exec.KillService("web", syscall.SIGKILL)
	waitForNoProcesses(t, exec)
}

func TestStartServiceAllowsReuseAfterKill(t *testing.T) {
	exec, _, root := testExecutor(t)
	if _, err := exec.StartService(context.Background(), ServiceRequest{Name: "web", Command: "sleep 30", CWD: root}); err != nil {
		t.Fatal(err)
	}
	if err := exec.KillService("web", syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitForNoProcesses(t, exec)
	if _, err := exec.StartService(context.Background(), ServiceRequest{Name: "web", Command: "sleep 30", CWD: root}); err != nil {
		t.Fatalf("expected restart under the same name to succeed, got: %v", err)
	}
	_ = exec.KillService("web", syscall.SIGKILL)
	waitForNoProcesses(t, exec)
}

func TestServiceStatusReportsCrashedWhenProcessDiesUnexpectedly(t *testing.T) {
	exec, _, root := testExecutor(t)
	svc, err := exec.StartService(context.Background(), ServiceRequest{Name: "flaky", Command: "exit 1", CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(exec.Processes("")) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	status, err := exec.ServiceStatus(context.Background(), svc.Name)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != ServiceStateCrashed {
		t.Fatalf("state = %q, want crashed", status.State)
	}
	if status.ExitCode == nil || *status.ExitCode != 1 {
		t.Fatalf("exit code = %v, want 1", status.ExitCode)
	}
}

func TestServiceStatusReportsStoppedAfterKillService(t *testing.T) {
	exec, _, root := testExecutor(t)
	svc, err := exec.StartService(context.Background(), ServiceRequest{Name: "web", Command: "sleep 30", CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.KillService(svc.Name, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitForNoProcesses(t, exec)
	// KillService removes the record outright (the name is freed for
	// reuse), so status on the now-unknown name must error clearly.
	if _, err := exec.ServiceStatus(context.Background(), svc.Name); err == nil {
		t.Fatal("expected unknown-service error after KillService removed the record")
	}
}

func TestServiceStatusAndKillUnknownNameError(t *testing.T) {
	exec, _, _ := testExecutor(t)
	if _, err := exec.ServiceStatus(context.Background(), "nope"); err == nil {
		t.Fatal("expected error for unknown service status")
	}
	if err := exec.KillService("nope", syscall.SIGTERM); err == nil {
		t.Fatal("expected error for unknown service kill")
	}
}

func TestLoadServicesMarksDeadPIDsStopped(t *testing.T) {
	exec, _, _ := testExecutor(t)
	servicesDir := t.TempDir()

	// A record left over from a daemon crash: still says "running", but its
	// PID (a made-up, almost certainly dead value) is gone.
	record := `{"name":"orphan","invocation_id":"inv_x","command":"sleep 30","session":"default","pid":999999,"started_at":"2026-08-17T00:00:00Z","state":"running"}`
	if err := os.WriteFile(filepath.Join(servicesDir, "orphan.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}

	alwaysDead := func(int) bool { return false }
	if err := exec.LoadServices(servicesDir, alwaysDead); err != nil {
		t.Fatal(err)
	}

	status, err := exec.ServiceStatus(context.Background(), "orphan")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != ServiceStateStopped {
		t.Fatalf("state = %q, want stopped", status.State)
	}

	// The on-disk record itself was rewritten too, not just the in-memory copy.
	data, err := os.ReadFile(filepath.Join(servicesDir, "orphan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"state":"stopped"`) {
		t.Fatalf("persisted record not updated: %s", data)
	}
}

func TestInvalidServiceNameRejected(t *testing.T) {
	exec, _, root := testExecutor(t)
	if _, err := exec.StartService(context.Background(), ServiceRequest{Name: "../escape", Command: "true", CWD: root}); err == nil {
		t.Fatal("expected error for a service name containing path traversal characters")
	}
}
