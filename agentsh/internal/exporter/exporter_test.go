package exporter

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/agentsh/internal/storage"
	"github.com/Fewbytes/sweatshop/agentsh/internal/workspace"
)

func TestExportWorkspaceParquetFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	paths, err := workspace.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}

	store, err := storage.Open(ctx, storage.Config{Path: paths.Database})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Create a successful test invocation
	started := time.Now().UTC().Add(-10 * time.Minute)
	ended := started.Add(2 * time.Second)
	exit0 := 0
	reasonOk := "ok"
	inv1 := storage.Invocation{
		ID:         "inv_001",
		Session:    "default",
		Command:    "go test ./pkg/...",
		CWD:        root,
		State:      storage.StateRunning,
		StartedAt:  started,
		DurationMS: 2000,
	}
	if err := store.CreateInvocation(ctx, inv1); err != nil {
		t.Fatal(err)
	}
	inv1.State = storage.StateExited
	inv1.EndedAt = &ended
	inv1.ExitCode = &exit0
	inv1.Reason = &reasonOk
	inv1.Stdout = storage.StreamRef{SHA256: "sha_out1", Bytes: 100, Lines: 5}
	inv1.Summary = &storage.CommandSummary{
		Family:  "go test",
		Status:  "passed",
		Passed:  5,
		Failed:  0,
		Skipped: 1,
		Total:   6,
	}
	if err := store.FinishInvocation(ctx, inv1); err != nil {
		t.Fatal(err)
	}

	// 2. Create a failed test invocation with failure frames and log templates
	exit1 := 1
	reasonFail := "nonzero"
	inv2 := storage.Invocation{
		ID:         "inv_002",
		Session:    "default",
		Command:    "pytest tests/",
		CWD:        root,
		State:      storage.StateRunning,
		StartedAt:  started.Add(5 * time.Minute),
		DurationMS: 1500,
	}
	if err := store.CreateInvocation(ctx, inv2); err != nil {
		t.Fatal(err)
	}
	inv2.State = storage.StateExited
	inv2.EndedAt = &ended
	inv2.ExitCode = &exit1
	inv2.Reason = &reasonFail
	inv2.Stdout = storage.StreamRef{SHA256: "sha_out2", Bytes: 250, Lines: 10}
	inv2.Summary = &storage.CommandSummary{
		Family: "pytest",
		Status: "failed",
		Passed: 2,
		Failed: 1,
		Total:  3,
		Failures: []storage.Failure{
			{
				Name:     "test_auth",
				Message:  "AssertionError: assert 1 == 2",
				Location: "tests/test_auth.py:15",
				Excerpt:  "E AssertionError: assert 1 == 2",
			},
		},
	}
	if err := store.FinishInvocation(ctx, inv2); err != nil {
		t.Fatal(err)
	}

	// Add log templates for inv_002
	templates := []storage.StoredLogTemplate{
		{
			InvocationID:   "inv_002",
			Stream:         "stdout",
			TemplateID:     "t_log_1",
			Template:       "Connecting to db at <IP>:<NUM>",
			Count:          8,
			FirstLine:      1,
			LastLine:       8,
			ExemplarOffset: 0,
			Exemplar:       "Connecting to db at 10.0.0.1:5432",
			Level:          "INFO",
		},
		{
			InvocationID:   "inv_002",
			Stream:         "stdout",
			TemplateID:     "t_log_2",
			Template:       "[stack trace: python at tests/test_auth.py:15]",
			Count:          1,
			FirstLine:      9,
			LastLine:       10,
			ExemplarOffset: 200,
			Exemplar:       "Traceback...",
			Level:          "ERROR",
			IsStackTrace:   true,
		},
	}
	if err := store.SaveLogTemplates(ctx, "inv_002", "stdout", templates); err != nil {
		t.Fatal(err)
	}

	store.Close()

	// 3. Run exporter
	outDir := filepath.Join(root, "exported_parquet")
	stats, err := ExportWorkspace(ctx, paths, outDir)
	if err != nil {
		t.Fatalf("ExportWorkspace failed: %v", err)
	}

	if stats.Invocations != 2 {
		t.Errorf("stats.Invocations = %d, want 2", stats.Invocations)
	}
	if stats.TestResults != 2 {
		t.Errorf("stats.TestResults = %d, want 2", stats.TestResults)
	}
	if stats.Errors != 1 {
		t.Errorf("stats.Errors = %d, want 1", stats.Errors)
	}
	if stats.Templates != 2 {
		t.Errorf("stats.Templates = %d, want 2", stats.Templates)
	}

	// 4. Verify parquet files exist on disk and can be read back
	invRows, err := ReadParquet[InvocationRecord](filepath.Join(outDir, "invocations.parquet"))
	if err != nil {
		t.Fatalf("ReadParquet invocations failed: %v", err)
	}
	if len(invRows) != 2 {
		t.Fatalf("invocations row count = %d, want 2", len(invRows))
	}

	testRows, err := ReadParquet[TestResultRecord](filepath.Join(outDir, "test_results.parquet"))
	if err != nil {
		t.Fatalf("ReadParquet test_results failed: %v", err)
	}
	if len(testRows) != 2 {
		t.Fatalf("test_results row count = %d, want 2", len(testRows))
	}

	errRows, err := ReadParquet[ErrorRecord](filepath.Join(outDir, "errors.parquet"))
	if err != nil {
		t.Fatalf("ReadParquet errors failed: %v", err)
	}
	if len(errRows) != 1 || errRows[0].ErrorName != "test_auth" {
		t.Fatalf("unexpected error records: %+v", errRows)
	}

	tmplRows, err := ReadParquet[TemplateRecord](filepath.Join(outDir, "templates.parquet"))
	if err != nil {
		t.Fatalf("ReadParquet templates failed: %v", err)
	}
	if len(tmplRows) != 2 {
		t.Fatalf("templates row count = %d, want 2", len(tmplRows))
	}
}

func TestExportEmptyWorkspace(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	paths, err := workspace.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(root, "empty_export")
	stats, err := ExportWorkspace(ctx, paths, outDir)
	if err != nil {
		t.Fatalf("ExportWorkspace on empty workspace failed: %v", err)
	}
	if stats.Invocations != 0 {
		t.Errorf("stats.Invocations = %d, want 0", stats.Invocations)
	}

	// Verify all 4 files were created
	for _, filename := range []string{"invocations.parquet", "templates.parquet", "errors.parquet", "test_results.parquet"} {
		if _, err := os.Stat(filepath.Join(outDir, filename)); err != nil {
			t.Errorf("expected %s to be created, got error: %v", filename, err)
		}
	}
}
