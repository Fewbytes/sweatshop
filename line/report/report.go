// Package report turns a witness check run into a JUnit XML report and a
// rendered HTML page for the object store, and records the run's duration and
// statistics in the master database (see design-docs/assembly-line-spec.md,
// storage table).
//
// Both destinations are interfaces: the object store (S3, MinIO, R2; the
// filesystem in dev) and the run database (Turso in v1). Reporting is
// best-effort for the caller: a failed upload never changes a verdict.
package report

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Check is the outcome of one required check.
type Check struct {
	Name       string
	Command    string
	ExitCode   int
	Duration   time.Duration
	EvidenceID string
	Output     string // evidence log, included in the failure body
}

// Run is one witness run over a commit.
type Run struct {
	Commit     string
	Tree       string
	Role       string
	AgentID    string
	Runner     string
	StartedAt  time.Time
	FinishedAt time.Time
	Checks     []Check
}

func (r Run) Passed() bool {
	for _, c := range r.Checks {
		if c.ExitCode != 0 {
			return false
		}
	}
	return true
}

func (r Run) Duration() time.Duration { return r.FinishedAt.Sub(r.StartedAt) }

// Stats is the row saved to the run database for Kaizen and andon workers.
type Stats struct {
	Commit     string
	Tree       string
	Role       string
	AgentID    string
	Runner     string
	StartedAt  time.Time
	DurationMS int64
	Checks     int
	Failures   int
	Passed     bool
	ReportKey  string // object-store key of the XML report
	HTMLKey    string // object-store key of the HTML report
}

// ObjectStore holds report blobs.
type ObjectStore interface {
	Put(ctx context.Context, key, contentType string, data []byte) error
}

// RunStore saves run duration and statistics to the master database.
type RunStore interface {
	RecordRun(ctx context.Context, s Stats) error
}

type testsuites struct {
	XMLName  xml.Name    `xml:"testsuites"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Time     string      `xml:"time,attr"`
	Suite    []testsuite `xml:"testsuite"`
}

type testsuite struct {
	Name      string     `xml:"name,attr"`
	Tests     int        `xml:"tests,attr"`
	Failures  int        `xml:"failures,attr"`
	Time      string     `xml:"time,attr"`
	Timestamp string     `xml:"timestamp,attr"`
	Cases     []testcase `xml:"testcase"`
}

type testcase struct {
	Name      string   `xml:"name,attr"`
	ClassName string   `xml:"classname,attr"`
	Time      string   `xml:"time,attr"`
	Failure   *failure `xml:"failure,omitempty"`
	SystemOut string   `xml:"system-out,omitempty"`
}

type failure struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

func seconds(d time.Duration) string { return fmt.Sprintf("%.3f", d.Seconds()) }

// JUnitXML renders the run as JUnit XML: one testcase per check.
func JUnitXML(r Run) ([]byte, error) {
	suite := testsuite{
		Name: r.Commit, Tests: len(r.Checks), Time: seconds(r.Duration()),
		Timestamp: r.StartedAt.UTC().Format(time.RFC3339),
	}
	for _, c := range r.Checks {
		tc := testcase{Name: c.Name, ClassName: "checks", Time: seconds(c.Duration)}
		if c.ExitCode != 0 {
			suite.Failures++
			tc.Failure = &failure{Message: fmt.Sprintf("%s exited %d", c.Command, c.ExitCode), Body: xmlSafe(c.Output)}
		}
		suite.Cases = append(suite.Cases, tc)
	}
	doc := testsuites{Tests: suite.Tests, Failures: suite.Failures, Time: suite.Time, Suite: []testsuite{suite}}
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(out, '\n')...), nil
}

// xmlSafe drops characters XML 1.0 cannot carry (check logs are raw output).
func xmlSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r', r >= 0x20 && r <= 0xD7FF, r >= 0xE000 && r <= 0xFFFD, r >= 0x10000:
			return r
		}
		return -1
	}, strings.ToValidUTF8(s, "�"))
}

// HTML renders the run as a self-contained page.
func HTML(r Run) []byte {
	var b strings.Builder
	verdict := "PASSED"
	if !r.Passed() {
		verdict = "FAILED"
	}
	fmt.Fprintf(&b, "<!doctype html>\n<html><head><meta charset=\"utf-8\"><title>%s %s</title></head><body>\n",
		html.EscapeString(shortCommit(r.Commit)), verdict)
	fmt.Fprintf(&b, "<h1>%s: %s</h1>\n", html.EscapeString(r.Commit), verdict)
	fmt.Fprintf(&b, "<p>runner %s, duration %s</p>\n<table border=\"1\"><tr><th>check</th><th>exit</th><th>seconds</th></tr>\n",
		html.EscapeString(r.Runner), seconds(r.Duration()))
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "<tr><td>%s</td><td>%d</td><td>%s</td></tr>\n", html.EscapeString(c.Name), c.ExitCode, seconds(c.Duration))
	}
	b.WriteString("</table>\n")
	for _, c := range r.Checks {
		if c.ExitCode != 0 {
			fmt.Fprintf(&b, "<h2>%s</h2>\n<pre>%s</pre>\n", html.EscapeString(c.Name), html.EscapeString(c.Output))
		}
	}
	b.WriteString("</body></html>\n")
	return []byte(b.String())
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// Reporter publishes a run to the object store and records its statistics.
type Reporter struct {
	Objects ObjectStore
	Runs    RunStore
}

// Publish uploads the XML and HTML reports (keyed by commit and finish time so
// repeated runs of one commit do not overwrite each other), then records the
// run. It tries every step and returns the joined errors.
func (p Reporter) Publish(ctx context.Context, r Run) error {
	prefix := fmt.Sprintf("reports/%s/%d", r.Commit, r.FinishedAt.UTC().UnixNano())
	stats := Stats{
		Commit: r.Commit, Tree: r.Tree, Role: r.Role, AgentID: r.AgentID, Runner: r.Runner,
		StartedAt: r.StartedAt.UTC(), DurationMS: r.Duration().Milliseconds(),
		Checks: len(r.Checks), Passed: r.Passed(),
	}
	for _, c := range r.Checks {
		if c.ExitCode != 0 {
			stats.Failures++
		}
	}
	var errs []error
	if p.Objects != nil {
		if xmlData, err := JUnitXML(r); err != nil {
			errs = append(errs, err)
		} else if err := p.Objects.Put(ctx, prefix+"/junit.xml", "application/xml", xmlData); err != nil {
			errs = append(errs, fmt.Errorf("put junit report: %w", err))
		} else {
			stats.ReportKey = prefix + "/junit.xml"
		}
		if err := p.Objects.Put(ctx, prefix+"/report.html", "text/html; charset=utf-8", HTML(r)); err != nil {
			errs = append(errs, fmt.Errorf("put html report: %w", err))
		} else {
			stats.HTMLKey = prefix + "/report.html"
		}
	}
	if p.Runs != nil {
		if err := p.Runs.RecordRun(ctx, stats); err != nil {
			errs = append(errs, fmt.Errorf("record run: %w", err))
		}
	}
	return errors.Join(errs...)
}

// FSStore is the dev object store: keys are paths under Root.
type FSStore struct{ Root string }

func (s FSStore) Put(_ context.Context, key, _ string, data []byte) error {
	clean := filepath.Clean(filepath.FromSlash(key))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("invalid object key %q", key)
	}
	path := filepath.Join(s.Root, clean)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// JSONLRunStore appends each run's statistics as one JSON line to Path. It is
// the dependency-free RunStore until the line has a database driver; Kaizen
// and andon workers (or an importer into the master database) read the file.
type JSONLRunStore struct {
	Path string
	mu   sync.Mutex
}

func (s *JSONLRunStore) RecordRun(_ context.Context, st Stats) error {
	line, err := json.Marshal(st)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ReadJSONL returns the runs saved by a JSONLRunStore, oldest first. A missing
// file is no runs; a torn or malformed line is skipped.
func ReadJSONL(path string) ([]Stats, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Stats
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var st Stats
		if json.Unmarshal(sc.Bytes(), &st) == nil {
			out = append(out, st)
		}
	}
	return out, sc.Err()
}
