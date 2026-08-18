package analyzer

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestVariableMaskingAndClustering(t *testing.T) {
	lines := []string{
		"2026-08-17T14:28:47.340Z INFO [server.go:123] Connection from 192.168.1.100:54321 with session 550e8400-e29b-41d4-a716-446655440000 handled in 42ms",
		"2026-08-17T14:28:48.100Z INFO [server.go:123] Connection from 10.0.0.1:12345 with session 123e4567-e89b-12d3-a456-426614174000 handled in 15ms",
		"2026-08-17T14:28:49.000Z ERROR [auth.go:45] Failed login for user 0xdeadbeef at /var/log/auth.log from https://auth.example.com/login",
	}

	content := strings.Join(lines, "\n")
	analysis := AnalyzeStream("inv_test_1", "stdout", content)

	if analysis.TotalLines != 3 {
		t.Fatalf("TotalLines = %d, want 3", analysis.TotalLines)
	}

	// First two lines should cluster into identical template
	if len(analysis.Templates) != 2 {
		t.Fatalf("Templates count = %d, want 2", len(analysis.Templates))
	}

	infoTmpl := analysis.Templates[1]
	if infoTmpl.Count != 2 {
		t.Errorf("info template count = %d, want 2", infoTmpl.Count)
	}
	if !strings.Contains(infoTmpl.Template, "<IP>") || !strings.Contains(infoTmpl.Template, "<UUID>") {
		t.Errorf("expected masked template, got: %q", infoTmpl.Template)
	}

	errTmpl := analysis.Templates[0] // Sorted first due to ERROR level
	if errTmpl.Count != 1 || errTmpl.Level != "ERROR" {
		t.Errorf("error template = %+v", errTmpl)
	}
}

func TestCollapseRepeatedLines(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 10000; i++ {
		fmt.Fprintf(&sb, "2026-08-17 14:00:%02d INFO [worker.go:50] Processed job %d successfully in %d ms\n", i%60, i, i*2)
	}

	analysis := AnalyzeStream("inv_big", "stdout", sb.String())
	if analysis.TotalLines != 10000 {
		t.Fatalf("TotalLines = %d, want 10000", analysis.TotalLines)
	}
	if len(analysis.Templates) != 1 {
		t.Fatalf("expected exactly 1 collapsed template for 10,000 repeated lines, got %d", len(analysis.Templates))
	}
	tmpl := analysis.Templates[0]
	if tmpl.Count != 10000 {
		t.Errorf("template count = %d, want 10000", tmpl.Count)
	}
	if tmpl.FirstLine != 1 || tmpl.LastLine != 10000 {
		t.Errorf("FirstLine=%d LastLine=%d, want 1..10000", tmpl.FirstLine, tmpl.LastLine)
	}
}

func TestStackTraceCollapse(t *testing.T) {
	t.Run("Go panic stack trace", func(t *testing.T) {
		logText := `2026/08/17 14:00:00 Starting app...
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: code=0x2 addr=0x0 pc=0x10a2b3c]

goroutine 1 [running]:
main.processOrder(0x0, 0x123)
	/app/pkg/orders/service.go:42 +0x68
main.main()
	/app/cmd/server/main.go:15 +0x34
2026/08/17 14:00:01 App shutdown.`

		analysis := AnalyzeStream("inv_go", "stderr", logText)
		foundST := false
		for _, tmpl := range analysis.Templates {
			if tmpl.IsStackTrace {
				foundST = true
				if !strings.Contains(tmpl.Template, "go") {
					t.Errorf("expected go stack trace template, got: %q", tmpl.Template)
				}
				if !strings.Contains(tmpl.Exemplar, "/app/pkg/orders/service.go:42") {
					t.Errorf("exemplar missing stack frame: %q", tmpl.Exemplar)
				}
			}
		}
		if !foundST {
			t.Fatal("expected collapsed go stack trace template")
		}
	})

	t.Run("Python traceback", func(t *testing.T) {
		logText := `Starting pytest run...
Traceback (most recent call last):
  File "/app/tests/test_api.py", line 25, in test_endpoint
    response = client.get("/api/v1/resource")
  File "/app/client.py", line 10, in get
    return self.session.request("GET", url)
ValueError: invalid connection pool
Finished test.`

		analysis := AnalyzeStream("inv_py", "stdout", logText)
		foundST := false
		for _, tmpl := range analysis.Templates {
			if tmpl.IsStackTrace {
				foundST = true
				if !strings.Contains(tmpl.Template, "python") {
					t.Errorf("expected python stack trace template, got: %q", tmpl.Template)
				}
				if !strings.Contains(tmpl.Exemplar, "ValueError") {
					t.Errorf("exemplar missing error type: %q", tmpl.Exemplar)
				}
			}
		}
		if !foundST {
			t.Fatal("expected collapsed python traceback template")
		}
	})

	t.Run("Node JS stack trace", func(t *testing.T) {
		logText := `TypeError: Cannot read property 'id' of undefined
    at handleRequest (/app/src/server.js:45:12)
    at Layer.handle [as handle_request] (/app/node_modules/express/lib/router/layer.js:95:5)
    at trim_prefix (/app/node_modules/express/lib/router/index.js:317:13)`

		analysis := AnalyzeStream("inv_node", "stderr", logText)
		if len(analysis.Templates) == 0 || !analysis.Templates[0].IsStackTrace {
			t.Fatalf("expected node stack trace template, got: %+v", analysis.Templates)
		}
	})
}

func TestLevelHistogram(t *testing.T) {
	logText := `INFO starting service
DEBUG initializing db pool
WARN high memory utilization detected
ERROR failed to connect to redis
FATAL unrecoverable panic encountered
INFO shutting down`

	analysis := AnalyzeStream("inv_levels", "stdout", logText)
	if analysis.Levels["INFO"] != 2 {
		t.Errorf("INFO count = %d, want 2", analysis.Levels["INFO"])
	}
	if analysis.Levels["DEBUG"] != 1 {
		t.Errorf("DEBUG count = %d, want 1", analysis.Levels["DEBUG"])
	}
	if analysis.Levels["WARN"] != 1 {
		t.Errorf("WARN count = %d, want 1", analysis.Levels["WARN"])
	}
	if analysis.Levels["ERROR"] != 1 {
		t.Errorf("ERROR count = %d, want 1", analysis.Levels["ERROR"])
	}
	if analysis.Levels["FATAL"] != 1 {
		t.Errorf("FATAL count = %d, want 1", analysis.Levels["FATAL"])
	}
}

func TestBaselineComparisonPrioritization(t *testing.T) {
	// Current run has 3 templates:
	// Template 1: Normal periodic heartbeat (seen 100 times in prior 5 runs)
	// Template 2: Known cache miss warning (seen 5 times in prior 5 runs)
	// Template 3: Brand new novel database error (never seen before!)
	current := &LogAnalysis{
		InvocationID: "inv_curr",
		Stream:       "stdout",
		TotalLines:   3,
		Templates: []LogTemplate{
			{ID: "t_heartbeat", Template: "Heartbeat ping OK", Count: 50, Level: "INFO"},
			{ID: "t_warn", Template: "Cache miss for key <NUM>", Count: 5, Level: "WARN"},
			{ID: "t_novel_err", Template: "Database connection dropped: <IP>", Count: 1, Level: "ERROR"},
		},
	}

	priorCounts := map[string]int{
		"t_heartbeat": 100,
		"t_warn":      5,
		// t_novel_err is not in priorCounts
	}

	BaselineComparison(current, priorCounts, 5)

	// Verify top template is the novel error template
	if len(current.Templates) != 3 {
		t.Fatalf("Templates count = %d, want 3", len(current.Templates))
	}

	top := current.Templates[0]
	if top.ID != "t_novel_err" || !top.Novel {
		t.Errorf("expected top template to be novel error, got: %+v", top)
	}

	if !strings.Contains(current.Summary, "1 novel template (1 error)") {
		t.Errorf("summary missing novel error description: %q", current.Summary)
	}
	if !strings.Contains(current.Summary, "5 prior run(s)") {
		t.Errorf("summary missing prior runs info: %q", current.Summary)
	}
}

// repeatingLineReader yields the same log line forever without materializing
// the whole stream in memory, standing in for a multi-GB blob.
type repeatingLineReader struct {
	line []byte
	buf  []byte
}

func (r *repeatingLineReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if len(r.buf) == 0 {
			r.buf = r.line
		}
		c := copy(p[n:], r.buf)
		r.buf = r.buf[c:]
		n += c
	}
	return n, nil
}

func TestAnalyzeReaderBoundsMemoryOnHugeStream(t *testing.T) {
	line := []byte("2026-08-17T14:28:47Z INFO [worker.go:50] Processed job 42 successfully\n")
	src := &repeatingLineReader{line: line}

	const maxBytes = 1 << 20 // 1MB cap, well under a synthetic ~1GB source
	analysis := AnalyzeReader("inv_huge", "stdout", src, maxBytes)

	if !analysis.Truncated {
		t.Fatal("expected Truncated=true for a stream exceeding the cap")
	}
	wantLines := maxBytes / len(line)
	if analysis.TotalLines < wantLines-1 || analysis.TotalLines > wantLines+1 {
		t.Errorf("TotalLines = %d, want ~%d (bounded by cap, not source size)", analysis.TotalLines, wantLines)
	}
}

func TestAnalyzeReaderNoTruncationWhenUnderCap(t *testing.T) {
	content := "one line\nanother line\n"
	analysis := AnalyzeReader("inv_small", "stdout", strings.NewReader(content), DefaultMaxAnalysisBytes)
	if analysis.Truncated {
		t.Error("expected Truncated=false when stream fits under the cap")
	}
	if analysis.TotalLines != 2 {
		t.Errorf("TotalLines = %d, want 2", analysis.TotalLines)
	}
}

var _ io.Reader = (*repeatingLineReader)(nil)

func TestEmptyAndDegradedStream(t *testing.T) {
	analysis := AnalyzeStream("inv_empty", "stdout", "")
	if analysis.TotalLines != 0 || len(analysis.Templates) != 0 {
		t.Fatalf("expected empty analysis on empty string: %+v", analysis)
	}
}
