package exporter

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/storage"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/workspace"
	parquet "github.com/parquet-go/parquet-go"
)

// InvocationRecord defines the Parquet schema for exported invocation metadata.
type InvocationRecord struct {
	ID           string `parquet:"id"`
	Session      string `parquet:"session"`
	Command      string `parquet:"command"`
	CWD          string `parquet:"cwd"`
	State        string `parquet:"state"`
	ExitCode     int32  `parquet:"exit_code"`
	Reason       string `parquet:"reason"`
	StartedAtMS  int64  `parquet:"started_at_unix_ms"`
	EndedAtMS    int64  `parquet:"ended_at_unix_ms"`
	DurationMS   int64  `parquet:"duration_ms"`
	StdoutSHA256 string `parquet:"stdout_sha256"`
	StdoutBytes  int64  `parquet:"stdout_bytes"`
	StdoutLines  int64  `parquet:"stdout_lines"`
	StderrSHA256 string `parquet:"stderr_sha256"`
	StderrBytes  int64  `parquet:"stderr_bytes"`
	StderrLines  int64  `parquet:"stderr_lines"`
	SummaryType  string `parquet:"summary_type"`
	SummaryState string `parquet:"summary_state"`
}

// TemplateRecord defines the Parquet schema for exported log template clusters.
type TemplateRecord struct {
	InvocationID   string `parquet:"invocation_id"`
	Stream         string `parquet:"stream"`
	TemplateID     string `parquet:"template_id"`
	Template       string `parquet:"template"`
	Count          int64  `parquet:"count"`
	FirstLine      int32  `parquet:"first_line"`
	LastLine       int32  `parquet:"last_line"`
	ExemplarOffset int64  `parquet:"exemplar_offset"`
	Exemplar       string `parquet:"exemplar"`
	Level          string `parquet:"level"`
	IsStackTrace   bool   `parquet:"is_stack_trace"`
}

// ErrorRecord defines the Parquet schema for extracted failure frames and errors.
type ErrorRecord struct {
	InvocationID string `parquet:"invocation_id"`
	Session      string `parquet:"session"`
	Command      string `parquet:"command"`
	Reason       string `parquet:"reason"`
	ExitCode     int32  `parquet:"exit_code"`
	TimestampMS  int64  `parquet:"timestamp_unix_ms"`
	ErrorName    string `parquet:"error_name"`
	ErrorMessage string `parquet:"error_message"`
	Location     string `parquet:"location"`
	Excerpt      string `parquet:"excerpt"`
}

// TestResultRecord defines the Parquet schema for structured test execution summaries.
type TestResultRecord struct {
	InvocationID string `parquet:"invocation_id"`
	Session      string `parquet:"session"`
	Command      string `parquet:"command"`
	Family       string `parquet:"family"`
	Status       string `parquet:"status"`
	Passed       int32  `parquet:"passed"`
	Failed       int32  `parquet:"failed"`
	Skipped      int32  `parquet:"skipped"`
	Total        int32  `parquet:"total"`
	Duration     string `parquet:"duration"`
	TimestampMS  int64  `parquet:"timestamp_unix_ms"`
}

// ExportStats reports counts of rows written across all 4 Parquet files.
type ExportStats struct {
	Invocations int    `json:"invocations"`
	Templates   int    `json:"templates"`
	Errors      int    `json:"errors"`
	TestResults int    `json:"test_results"`
	OutputDir   string `json:"output_dir"`
}

// ExportWorkspace reads Turso/SQLite invocation history, log template clusters,
// structured test summaries, and errors from workspace paths, writing 4 Parquet files
// to outDir.
func ExportWorkspace(ctx context.Context, paths workspace.Paths, outDir string) (*ExportStats, error) {
	if outDir == "" {
		outDir = filepath.Join(paths.StateDir, "analytics")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("create export output directory: %w", err)
	}

	store, err := storage.Open(ctx, storage.Config{Path: paths.Database})
	if err != nil {
		return nil, fmt.Errorf("open history database: %w", err)
	}
	defer store.Close()

	// Load all invocations (up to 10000)
	invocations, err := store.History(ctx, "", "", "", nil, 10000)
	if err != nil {
		return nil, fmt.Errorf("query invocations: %w", err)
	}

	// Prepare records
	var invRecords []InvocationRecord
	var errRecords []ErrorRecord
	var testRecords []TestResultRecord
	var tmplRecords []TemplateRecord

	for _, inv := range invocations {
		var startedMS, endedMS int64
		if !inv.StartedAt.IsZero() {
			startedMS = inv.StartedAt.UnixMilli()
		}
		if inv.EndedAt != nil {
			endedMS = inv.EndedAt.UnixMilli()
		}
		exitCode := int32(-1)
		if inv.ExitCode != nil {
			exitCode = int32(*inv.ExitCode)
		}
		reason := ""
		if inv.Reason != nil {
			reason = *inv.Reason
		}

		sumType, sumState := "", ""
		if inv.Summary != nil {
			sumType = inv.Summary.Family
			sumState = inv.Summary.Status

			// Extract test results if it's a test runner
			if inv.Summary.Total > 0 || inv.Summary.Family == "go test" || inv.Summary.Family == "pytest" || inv.Summary.Family == "jest" || (inv.Summary.Family == "cargo" && inv.Summary.Total > 0) {
				testRecords = append(testRecords, TestResultRecord{
					InvocationID: inv.ID,
					Session:      inv.Session,
					Command:      inv.Command,
					Family:       inv.Summary.Family,
					Status:       inv.Summary.Status,
					Passed:       int32(inv.Summary.Passed),
					Failed:       int32(inv.Summary.Failed),
					Skipped:      int32(inv.Summary.Skipped),
					Total:        int32(inv.Summary.Total),
					Duration:     inv.Summary.Duration,
					TimestampMS:  startedMS,
				})
			}

			// Extract structured failure frames
			for _, f := range inv.Summary.Failures {
				errRecords = append(errRecords, ErrorRecord{
					InvocationID: inv.ID,
					Session:      inv.Session,
					Command:      inv.Command,
					Reason:       reason,
					ExitCode:     exitCode,
					TimestampMS:  startedMS,
					ErrorName:    f.Name,
					ErrorMessage: f.Message,
					Location:     f.Location,
					Excerpt:      f.Excerpt,
				})
			}
		} else if exitCode != 0 && reason != "ok" {
			// Generic error record for non-structured failed invocation
			errRecords = append(errRecords, ErrorRecord{
				InvocationID: inv.ID,
				Session:      inv.Session,
				Command:      inv.Command,
				Reason:       reason,
				ExitCode:     exitCode,
				TimestampMS:  startedMS,
				ErrorMessage: fmt.Sprintf("invocation failed with state=%s reason=%s exit=%d", inv.State, reason, exitCode),
			})
		}

		invRecords = append(invRecords, InvocationRecord{
			ID:           inv.ID,
			Session:      inv.Session,
			Command:      inv.Command,
			CWD:          inv.CWD,
			State:        string(inv.State),
			ExitCode:     exitCode,
			Reason:       reason,
			StartedAtMS:  startedMS,
			EndedAtMS:    endedMS,
			DurationMS:   inv.DurationMS,
			StdoutSHA256: inv.Stdout.SHA256,
			StdoutBytes:  inv.Stdout.Bytes,
			StdoutLines:  inv.Stdout.Lines,
			StderrSHA256: inv.Stderr.SHA256,
			StderrBytes:  inv.Stderr.Bytes,
			StderrLines:  inv.Stderr.Lines,
			SummaryType:  sumType,
			SummaryState: sumState,
		})

		// Load stored log templates for stdout and stderr
		for _, stream := range []string{"stdout", "stderr"} {
			templates, err := store.GetLogTemplates(ctx, inv.ID, stream)
			if err == nil {
				for _, t := range templates {
					tmplRecords = append(tmplRecords, TemplateRecord{
						InvocationID:   t.InvocationID,
						Stream:         t.Stream,
						TemplateID:     t.TemplateID,
						Template:       t.Template,
						Count:          int64(t.Count),
						FirstLine:      int32(t.FirstLine),
						LastLine:       int32(t.LastLine),
						ExemplarOffset: t.ExemplarOffset,
						Exemplar:       t.Exemplar,
						Level:          t.Level,
						IsStackTrace:   t.IsStackTrace,
					})
				}
			}
		}
	}

	// Write the 4 Parquet files
	if err := WriteParquet(filepath.Join(outDir, "invocations.parquet"), invRecords); err != nil {
		return nil, fmt.Errorf("write invocations.parquet: %w", err)
	}
	if err := WriteParquet(filepath.Join(outDir, "templates.parquet"), tmplRecords); err != nil {
		return nil, fmt.Errorf("write templates.parquet: %w", err)
	}
	if err := WriteParquet(filepath.Join(outDir, "errors.parquet"), errRecords); err != nil {
		return nil, fmt.Errorf("write errors.parquet: %w", err)
	}
	if err := WriteParquet(filepath.Join(outDir, "test_results.parquet"), testRecords); err != nil {
		return nil, fmt.Errorf("write test_results.parquet: %w", err)
	}

	return &ExportStats{
		Invocations: len(invRecords),
		Templates:   len(tmplRecords),
		Errors:      len(errRecords),
		TestResults: len(testRecords),
		OutputDir:   outDir,
	}, nil
}

// WriteParquet writes a slice of structs to a Parquet file.
func WriteParquet[T any](path string, rows []T) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := parquet.NewGenericWriter[T](file)
	if len(rows) > 0 {
		if _, err := writer.Write(rows); err != nil {
			_ = writer.Close()
			return err
		}
	}
	return writer.Close()
}

// ReadParquet reads rows back from a Parquet file for verification and testing.
func ReadParquet[T any](path string) ([]T, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := parquet.NewGenericReader[T](file)
	defer reader.Close()

	rows := make([]T, reader.NumRows())
	n, err := reader.Read(rows)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return rows[:n], nil
}
