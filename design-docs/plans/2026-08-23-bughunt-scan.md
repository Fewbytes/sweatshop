# bughunt `scan` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `bughunt init` and `bughunt scan` — a deterministic, LLM-free static analysis gate that records findings with stable fingerprints in a Dolt database.

**Architecture:** A new Go module `bughunt/`, sibling to `agentsh/`, built as a single binary `bin/bughunt`. Detectors are adapters behind one interface, each translating a native tool's output into normalized `Hit` values. Hits are assigned fingerprints from the enclosing symbol plus normalized match text, then upserted into a Dolt-backed store. `scan` exits non-zero only for findings whose status is absent, `new`, or `confirmed`.

**Tech Stack:** Go 1.24.9, Dolt (embedded, via `database/sql`), `gopkg.in/yaml.v3`, `go/parser` for Go symbol resolution, external detector binaries (`go vet`, `staticcheck`, `errcheck`, `ineffassign`, `gosec`, `ast-grep`).

**Spec:** `design-docs/bughunt-spec.md`

## Global Constraints

- Go module path: `github.com/Fewbytes/sweatshop/bughunt`. Go version `1.24.9` (matches `agentsh/go.mod`).
- Single binary, built to `bin/bughunt` by `just build`.
- Never call an LLM anywhere in this plan. `scan` is deterministic by definition.
- A missing detector binary is a warning, never an error. A scan with zero available detectors still succeeds and records a run.
- Line numbers are display-only and MUST NOT enter any fingerprint.
- All new recipes go in the root `justfile`; follow its existing `cd <dir> && go ...` style.
- Every task ends with `cd bughunt && go test ./...` passing and a commit.
- Commit messages: Conventional Commits, matching repo history (`feat:`, `fix:`, `refactor:`).

## Deviation from spec, deliberate

The spec names tree-sitter (or ctags) for symbol resolution. This plan implements Go symbol resolution with the standard library's `go/parser` instead: v1 is Go-only, the stdlib is exact for Go, and it avoids a cgo dependency in the first working version. `internal/symbol` is an interface from Task 4 onward, so tree-sitter enters as an additional implementation when the second language does, with no call-site changes.

---

### Task 1: Module scaffold and binary skeleton

**Files:**
- Create: `bughunt/go.mod`
- Create: `bughunt/cmd/bughunt/main.go`
- Create: `bughunt/internal/version/version.go`
- Test: `bughunt/cmd/bughunt/main_test.go`
- Modify: `justfile:11-14` (build recipe), `justfile:17-18` (test), `justfile:23-26` (lint), `justfile:30-34` (install)

**Interfaces:**
- Consumes: nothing.
- Produces: `version.Version`, `version.Commit` (both `string`); binary `bin/bughunt` responding to `bughunt version`.

- [ ] **Step 1: Write the failing test**

Create `bughunt/cmd/bughunt/main_test.go`:

```go
package main

import (
	"bytes"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"version"}, &out, &out)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if out.Len() == 0 {
		t.Fatal("version printed nothing")
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"nope"}, &out, &out)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd bughunt && go test ./cmd/bughunt/
```

Expected: FAIL — no `go.mod` yet, or `undefined: run`.

- [ ] **Step 3: Create the module**

```bash
cd bughunt && go mod init github.com/Fewbytes/sweatshop/bughunt && go mod edit -go=1.24.9
```

- [ ] **Step 4: Write the version package**

Create `bughunt/internal/version/version.go`:

```go
// Package version carries build metadata injected at link time.
package version

var (
	Version = "dev"
	Commit  = "none"
)
```

- [ ] **Step 5: Write the minimal main**

Create `bughunt/cmd/bughunt/main.go`:

```go
// Command bughunt finds bugs, records them, and turns them into static checks.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/Fewbytes/sweatshop/bughunt/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one command and returns the process exit code. Exit codes:
// 0 success, 1 findings that gate, 2 usage error, 3 internal error.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "bughunt %s (%s)\n", version.Version, version.Commit)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s\n", args[0], usage)
		return 2
	}
}

const usage = `usage: bughunt <command> [flags]

commands:
  version   print version and exit`
```

- [ ] **Step 6: Run test to verify it passes**

```bash
cd bughunt && go test ./cmd/bughunt/
```

Expected: PASS.

- [ ] **Step 7: Wire the justfile**

In `justfile`, add `bughunt_dir := "bughunt"` next to `agentsh_dir`, then extend the existing recipes. `build` gains:

```make
    cd {{bughunt_dir}} && go build -ldflags '-X github.com/Fewbytes/sweatshop/bughunt/internal/version.Version={{version}} -X github.com/Fewbytes/sweatshop/bughunt/internal/version.Commit={{commit}}' -o ../{{bin_dir}}/bughunt ./cmd/bughunt
```

`test` gains `cd {{bughunt_dir}} && go test ./...`. `lint` gains the same three checks the agentsh block runs (`go vet ./...`, the `gofmt -l .` guard, `go mod tidy && git diff --exit-code go.mod go.sum`), with `{{bughunt_dir}}` substituted. `install` gains the matching `go build -o {{dest}}/bughunt ./cmd/bughunt` line.

- [ ] **Step 8: Verify the whole build**

```bash
just build && ./bin/bughunt version && just lint
```

Expected: version string prints; lint clean.

- [ ] **Step 9: Commit**

```bash
git add bughunt justfile
git commit -m "feat(bughunt): module scaffold and version command"
```

---

### Task 2: Config file

**Files:**
- Create: `bughunt/internal/config/config.go`
- Test: `bughunt/internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Config struct { Detectors map[string]bool; SeverityFloor string; Exclude []string }`
  - `func Load(dir string) (Config, error)` — reads `<dir>/config.yaml`; a missing file returns `Default()`, not an error.
  - `func Default() Config`
  - `func (c Config) DetectorEnabled(name string) bool` — absent from the map means enabled.
  - `func (c Config) Excluded(path string) bool`

- [ ] **Step 1: Write the failing test**

Create `bughunt/internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileReturnsDefault(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.SeverityFloor != "low" {
		t.Fatalf("SeverityFloor = %q, want %q", c.SeverityFloor, "low")
	}
	if !c.DetectorEnabled("govet") {
		t.Fatal("govet should be enabled by default")
	}
}

func TestLoadDisablesDetector(t *testing.T) {
	dir := t.TempDir()
	body := "detectors:\n  gosec: false\nseverity_floor: medium\nexclude:\n  - vendor/**\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DetectorEnabled("gosec") {
		t.Fatal("gosec should be disabled")
	}
	if !c.DetectorEnabled("govet") {
		t.Fatal("govet unmentioned, should stay enabled")
	}
	if c.SeverityFloor != "medium" {
		t.Fatalf("SeverityFloor = %q, want medium", c.SeverityFloor)
	}
	if !c.Excluded("vendor/github.com/x/y.go") {
		t.Fatal("vendor path should be excluded")
	}
	if c.Excluded("internal/scan/scan.go") {
		t.Fatal("internal path should not be excluded")
	}
}

func TestLoadEmptyFileIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.DetectorEnabled("staticcheck") {
		t.Fatal("empty config means auto-detect everything")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd bughunt && go test ./internal/config/
```

Expected: FAIL — package does not exist.

- [ ] **Step 3: Add the yaml dependency**

```bash
cd bughunt && go get gopkg.in/yaml.v3@v3.0.1
```

- [ ] **Step 4: Write the implementation**

Create `bughunt/internal/config/config.go`:

```go
// Package config loads .bughunt/config.yaml. An absent or empty file is valid
// and means: enable every detector that is installed, exclude nothing.
package config

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config mirrors .bughunt/config.yaml.
type Config struct {
	// Detectors maps a detector name to whether it is enabled. A detector
	// absent from the map is enabled.
	Detectors map[string]bool `yaml:"detectors"`
	// SeverityFloor drops hits below this severity: low, medium, or high.
	SeverityFloor string `yaml:"severity_floor"`
	// Exclude holds glob patterns matched against slash-separated repo paths.
	// A trailing /** matches the directory and everything under it.
	Exclude []string `yaml:"exclude"`
}

// Default returns the configuration used when no file is present.
func Default() Config {
	return Config{Detectors: map[string]bool{}, SeverityFloor: "low"}
}

// Load reads <dir>/config.yaml. A missing file yields Default().
func Load(dir string) (Config, error) {
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}
	c := Default()
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	if c.Detectors == nil {
		c.Detectors = map[string]bool{}
	}
	if c.SeverityFloor == "" {
		c.SeverityFloor = "low"
	}
	return c, nil
}

// DetectorEnabled reports whether the named detector should run.
func (c Config) DetectorEnabled(name string) bool {
	enabled, ok := c.Detectors[name]
	return !ok || enabled
}

// Excluded reports whether a repo-relative path matches an exclude pattern.
func (c Config) Excluded(p string) bool {
	p = filepath.ToSlash(p)
	for _, pattern := range c.Exclude {
		if dir, ok := strings.CutSuffix(pattern, "/**"); ok {
			if p == dir || strings.HasPrefix(p, dir+"/") {
				return true
			}
			continue
		}
		if ok, _ := path.Match(pattern, p); ok {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run test to verify it passes**

```bash
cd bughunt && go test ./internal/config/
```

Expected: PASS, all three tests.

- [ ] **Step 6: Commit**

```bash
git add bughunt/internal/config bughunt/go.mod bughunt/go.sum
git commit -m "feat(bughunt): config loading with enable-by-default detectors"
```

---

### Task 3: Store interface and schema, on SQLite

The store is defined against `database/sql` so it is driver-agnostic. Task 9 swaps the driver to Dolt without touching this package's API. Developing against SQLite first keeps every test in this plan fast and hermetic.

**Files:**
- Create: `bughunt/internal/store/store.go`
- Create: `bughunt/internal/store/schema.sql`
- Test: `bughunt/internal/store/store_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Status string` with constants `StatusNew`, `StatusConfirmed`, `StatusFixed`, `StatusUnreproducible`, `StatusSuppressed`, `StatusEscalated` (values `"new"`, `"confirmed"`, `"fixed"`, `"unreproducible"`, `"suppressed"`, `"escalated"`).
  - `func (s Status) Gates() bool` — true for `StatusNew` and `StatusConfirmed` only.
  - `type Finding struct { Fingerprint, RuleID, Detector, File string; Line int; Symbol, Message, Severity string; Confidence float64; Status Status; FirstSeenRun, LastSeenRun string; SeenCount int; TestPath string; TestKept bool; FixCommit, Branch, PRURL, BeadID, TriageNote string }`
  - `type Run struct { ID, StartedAt, CommitSHA, Mode, DiffBase string; DetectorsUsed, DetectorsUnavailable []string; AgentshInvocationIDs []string; FindingCount, NewCount int }`
  - `type Store struct{ ... }`
  - `func Open(dsn string) (*Store, error)`, `func (s *Store) Close() error`
  - `func (s *Store) Migrate(ctx context.Context) error`
  - `func (s *Store) InsertRun(ctx context.Context, r Run) error`
  - `func (s *Store) UpsertFinding(ctx context.Context, f Finding) (Status, error)` — returns the status the finding holds after the upsert. New rows are inserted with `StatusNew`; existing rows keep their status and get `LastSeenRun`/`Line`/`SeenCount` updated.
  - `func (s *Store) SetStatus(ctx context.Context, fingerprint string, status Status, note string) error`
  - `func (s *Store) GetFinding(ctx context.Context, fingerprint string) (Finding, bool, error)`
  - `func (s *Store) ListFindings(ctx context.Context, status Status) ([]Finding, error)` — empty `status` lists all.

- [ ] **Step 1: Write the schema**

Create `bughunt/internal/store/schema.sql`. Written in the SQL subset both SQLite and Dolt/MySQL accept:

```sql
CREATE TABLE IF NOT EXISTS run (
  id                     VARCHAR(64) PRIMARY KEY,
  started_at             VARCHAR(32) NOT NULL,
  commit_sha             VARCHAR(64) NOT NULL,
  mode                   VARCHAR(16) NOT NULL,
  diff_base              VARCHAR(64) NOT NULL,
  detectors_used         TEXT NOT NULL,
  detectors_unavailable  TEXT NOT NULL,
  agentsh_invocation_ids TEXT NOT NULL,
  finding_count          INT NOT NULL,
  new_count              INT NOT NULL
);

CREATE TABLE IF NOT EXISTS finding (
  fingerprint    VARCHAR(64) PRIMARY KEY,
  rule_id        VARCHAR(255) NOT NULL,
  detector       VARCHAR(64) NOT NULL,
  file           TEXT NOT NULL,
  line           INT NOT NULL,
  symbol         TEXT NOT NULL,
  message        TEXT NOT NULL,
  severity       VARCHAR(16) NOT NULL,
  confidence     DOUBLE NOT NULL,
  status         VARCHAR(16) NOT NULL,
  first_seen_run VARCHAR(64) NOT NULL,
  last_seen_run  VARCHAR(64) NOT NULL,
  seen_count     INT NOT NULL,
  test_path      TEXT NOT NULL,
  test_kept      BOOLEAN NOT NULL,
  fix_commit     VARCHAR(64) NOT NULL,
  branch         TEXT NOT NULL,
  pr_url         TEXT NOT NULL,
  bead_id        VARCHAR(64) NOT NULL,
  triage_note    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS rule (
  id                VARCHAR(255) PRIMARY KEY,
  engine            VARCHAR(32) NOT NULL,
  path              TEXT NOT NULL,
  origin_finding    VARCHAR(64) NOT NULL,
  created_at        VARCHAR(32) NOT NULL,
  hit_count         INT NOT NULL,
  tp_count          INT NOT NULL,
  fp_count          INT NOT NULL,
  status            VARCHAR(16) NOT NULL,
  retirement_reason TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS lesson (
  class      VARCHAR(255) PRIMARY KEY,
  doc_path   TEXT NOT NULL,
  rule_ids   TEXT NOT NULL,
  updated_at VARCHAR(32) NOT NULL
);
```

`rule` and `lesson` are created now and used by Plan 2. Creating them here keeps migration in one place.

- [ ] **Step 2: Write the failing test**

Create `bughunt/internal/store/store_test.go`:

```go
package store

import (
	"context"
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := open(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

func TestUpsertFindingInsertsAsNew(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	f := Finding{Fingerprint: "abc", RuleID: "govet/printf", Detector: "govet", File: "a.go", Line: 10, FirstSeenRun: "r1", LastSeenRun: "r1"}
	got, err := s.UpsertFinding(ctx, f)
	if err != nil {
		t.Fatalf("UpsertFinding: %v", err)
	}
	if got != StatusNew {
		t.Fatalf("status = %q, want new", got)
	}
	stored, ok, err := s.GetFinding(ctx, "abc")
	if err != nil || !ok {
		t.Fatalf("GetFinding: %v ok=%v", err, ok)
	}
	if stored.SeenCount != 1 {
		t.Fatalf("SeenCount = %d, want 1", stored.SeenCount)
	}
}

func TestUpsertFindingPreservesStatusAndBumpsSeenCount(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	f := Finding{Fingerprint: "abc", RuleID: "r", Detector: "govet", File: "a.go", Line: 10, FirstSeenRun: "r1", LastSeenRun: "r1"}
	if _, err := s.UpsertFinding(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(ctx, "abc", StatusSuppressed, "known false positive"); err != nil {
		t.Fatal(err)
	}

	f.Line = 42
	f.LastSeenRun = "r2"
	got, err := s.UpsertFinding(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if got != StatusSuppressed {
		t.Fatalf("status = %q, want suppressed — upsert must not resurrect a triaged finding", got)
	}
	stored, _, err := s.GetFinding(ctx, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if stored.SeenCount != 2 {
		t.Fatalf("SeenCount = %d, want 2", stored.SeenCount)
	}
	if stored.Line != 42 {
		t.Fatalf("Line = %d, want 42 — line is display data and must track the latest sighting", stored.Line)
	}
	if stored.FirstSeenRun != "r1" {
		t.Fatalf("FirstSeenRun = %q, want r1", stored.FirstSeenRun)
	}
}

func TestStatusGates(t *testing.T) {
	gating := []Status{StatusNew, StatusConfirmed}
	quiet := []Status{StatusFixed, StatusUnreproducible, StatusSuppressed, StatusEscalated}
	for _, s := range gating {
		if !s.Gates() {
			t.Errorf("%q should gate", s)
		}
	}
	for _, s := range quiet {
		if s.Gates() {
			t.Errorf("%q should not gate", s)
		}
	}
}

func TestInsertRunAndList(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	r := Run{ID: "r1", StartedAt: "2026-08-23T00:00:00Z", CommitSHA: "deadbeef", Mode: "scan",
		DetectorsUsed: []string{"govet"}, DetectorsUnavailable: []string{"gosec"}, FindingCount: 1, NewCount: 1}
	if err := s.InsertRun(ctx, r); err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	if _, err := s.UpsertFinding(ctx, Finding{Fingerprint: "x", RuleID: "r", Detector: "govet", File: "a.go", FirstSeenRun: "r1", LastSeenRun: "r1"}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListFindings(ctx, "")
	if err != nil || len(all) != 1 {
		t.Fatalf("ListFindings: %v len=%d", err, len(all))
	}
	none, err := s.ListFindings(ctx, StatusFixed)
	if err != nil || len(none) != 0 {
		t.Fatalf("ListFindings(fixed): %v len=%d", err, len(none))
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

```bash
cd bughunt && go test ./internal/store/
```

Expected: FAIL — package does not exist.

- [ ] **Step 4: Add the SQLite driver**

Pure Go, no cgo, so tests stay hermetic:

```bash
cd bughunt && go get modernc.org/sqlite@latest
```

- [ ] **Step 5: Write the implementation**

Create `bughunt/internal/store/store.go`:

```go
// Package store persists runs, findings, and rules. The API is written against
// database/sql so the underlying engine can change without touching callers.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// Status is the triage state of a finding.
type Status string

const (
	StatusNew            Status = "new"
	StatusConfirmed      Status = "confirmed"
	StatusFixed          Status = "fixed"
	StatusUnreproducible Status = "unreproducible"
	StatusSuppressed     Status = "suppressed"
	StatusEscalated      Status = "escalated"
)

// Gates reports whether a finding in this status causes scan to exit non-zero.
// Only untriaged and confirmed-open findings gate; everything else is either
// resolved, dismissed, or already tracked as work elsewhere.
func (s Status) Gates() bool {
	return s == StatusNew || s == StatusConfirmed
}

// Finding is one deduplicated problem, identified by fingerprint.
type Finding struct {
	Fingerprint  string
	RuleID       string
	Detector     string
	File         string
	Line         int
	Symbol       string
	Message      string
	Severity     string
	Confidence   float64
	Status       Status
	FirstSeenRun string
	LastSeenRun  string
	SeenCount    int
	TestPath     string
	TestKept     bool
	FixCommit    string
	Branch       string
	PRURL        string
	BeadID       string
	TriageNote   string
}

// Run is one execution of scan or hunt.
type Run struct {
	ID                   string
	StartedAt            string
	CommitSHA            string
	Mode                 string
	DiffBase             string
	DetectorsUsed        []string
	DetectorsUnavailable []string
	AgentshInvocationIDs []string
	FindingCount         int
	NewCount             int
}

// Store is a handle to the bughunt database.
type Store struct{ db *sql.DB }

// Open connects to the database at dsn.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Migrate creates any missing tables. Safe to call repeatedly.
func (s *Store) Migrate(ctx context.Context) error {
	for _, stmt := range strings.Split(schema, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// InsertRun records one run.
func (s *Store) InsertRun(ctx context.Context, r Run) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO run (id, started_at, commit_sha, mode, diff_base, detectors_used,
		 detectors_unavailable, agentsh_invocation_ids, finding_count, new_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.StartedAt, r.CommitSHA, r.Mode, r.DiffBase,
		strings.Join(r.DetectorsUsed, ","), strings.Join(r.DetectorsUnavailable, ","),
		strings.Join(r.AgentshInvocationIDs, ","), r.FindingCount, r.NewCount)
	return err
}

// UpsertFinding inserts a finding as new, or records another sighting of one
// already known. An existing finding keeps its triage status: a scan must never
// resurrect something a human already dismissed. It returns the status the
// finding holds after the write.
func (s *Store) UpsertFinding(ctx context.Context, f Finding) (Status, error) {
	existing, ok, err := s.GetFinding(ctx, f.Fingerprint)
	if err != nil {
		return "", err
	}
	if ok {
		_, err := s.db.ExecContext(ctx,
			`UPDATE finding SET line = ?, file = ?, message = ?, last_seen_run = ?,
			 seen_count = seen_count + 1 WHERE fingerprint = ?`,
			f.Line, f.File, f.Message, f.LastSeenRun, f.Fingerprint)
		if err != nil {
			return "", err
		}
		return existing.Status, nil
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO finding (fingerprint, rule_id, detector, file, line, symbol, message,
		 severity, confidence, status, first_seen_run, last_seen_run, seen_count,
		 test_path, test_kept, fix_commit, branch, pr_url, bead_id, triage_note)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, '', false, '', '', '', '', '')`,
		f.Fingerprint, f.RuleID, f.Detector, f.File, f.Line, f.Symbol, f.Message,
		f.Severity, f.Confidence, StatusNew, f.FirstSeenRun, f.LastSeenRun)
	if err != nil {
		return "", err
	}
	return StatusNew, nil
}

// SetStatus records a triage decision.
func (s *Store) SetStatus(ctx context.Context, fingerprint string, status Status, note string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE finding SET status = ?, triage_note = ? WHERE fingerprint = ?`,
		status, note, fingerprint)
	return err
}

const findingColumns = `fingerprint, rule_id, detector, file, line, symbol, message,
 severity, confidence, status, first_seen_run, last_seen_run, seen_count,
 test_path, test_kept, fix_commit, branch, pr_url, bead_id, triage_note`

func scanFinding(rows interface{ Scan(...any) error }) (Finding, error) {
	var f Finding
	err := rows.Scan(&f.Fingerprint, &f.RuleID, &f.Detector, &f.File, &f.Line, &f.Symbol,
		&f.Message, &f.Severity, &f.Confidence, &f.Status, &f.FirstSeenRun, &f.LastSeenRun,
		&f.SeenCount, &f.TestPath, &f.TestKept, &f.FixCommit, &f.Branch, &f.PRURL,
		&f.BeadID, &f.TriageNote)
	return f, err
}

// GetFinding returns one finding by fingerprint.
func (s *Store) GetFinding(ctx context.Context, fingerprint string) (Finding, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+findingColumns+` FROM finding WHERE fingerprint = ?`, fingerprint)
	f, err := scanFinding(row)
	if err == sql.ErrNoRows {
		return Finding{}, false, nil
	}
	if err != nil {
		return Finding{}, false, err
	}
	return f, true, nil
}

// ListFindings returns findings, optionally filtered by status.
func (s *Store) ListFindings(ctx context.Context, status Status) ([]Finding, error) {
	query := `SELECT ` + findingColumns + ` FROM finding`
	args := []any{}
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, string(status))
	}
	query += ` ORDER BY fingerprint`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Finding
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
```

- [ ] **Step 6: Run test to verify it passes**

```bash
cd bughunt && go test ./internal/store/
```

Expected: PASS, all five tests.

- [ ] **Step 7: Commit**

```bash
git add bughunt/internal/store bughunt/go.mod bughunt/go.sum
git commit -m "feat(bughunt): store schema, finding upsert, gate semantics"
```

---

### Task 4: Symbol resolution

**Files:**
- Create: `bughunt/internal/symbol/symbol.go`
- Create: `bughunt/internal/symbol/golang.go`
- Test: `bughunt/internal/symbol/golang_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Resolver interface { Enclosing(file string, line int) (string, bool) }`
  - `func NewGo() Resolver` — resolves Go source with `go/parser`. Returns names like `funcName`, `Type.methodName`, or `var blockName` for package-level declarations. Second return is false when the file cannot be parsed or the line is outside any declaration.
  - `func Resolve(r Resolver, file string, line int) string` — convenience wrapper returning `""` when unresolved.

- [ ] **Step 1: Write the failing test**

Create `bughunt/internal/symbol/golang_test.go`:

```go
package symbol

import (
	"os"
	"path/filepath"
	"testing"
)

const src = `package sample

import "fmt"

var topLevel = 1

func Alpha() {
	fmt.Println("a")
}

type T struct{}

func (t *T) Beta() {
	fmt.Println("b")
}
`

func writeSample(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sample.go")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnclosingFunction(t *testing.T) {
	p := writeSample(t)
	r := NewGo()
	got, ok := r.Enclosing(p, 8) // fmt.Println("a")
	if !ok || got != "Alpha" {
		t.Fatalf("Enclosing = %q, %v; want \"Alpha\", true", got, ok)
	}
}

func TestEnclosingMethodIncludesReceiver(t *testing.T) {
	p := writeSample(t)
	got, ok := NewGo().Enclosing(p, 14) // fmt.Println("b")
	if !ok || got != "T.Beta" {
		t.Fatalf("Enclosing = %q, %v; want \"T.Beta\", true", got, ok)
	}
}

func TestEnclosingPackageLevelVar(t *testing.T) {
	p := writeSample(t)
	got, ok := NewGo().Enclosing(p, 5)
	if !ok || got != "topLevel" {
		t.Fatalf("Enclosing = %q, %v; want \"topLevel\", true", got, ok)
	}
}

func TestEnclosingOutsideAnyDeclaration(t *testing.T) {
	p := writeSample(t)
	if _, ok := NewGo().Enclosing(p, 3); ok {
		t.Fatal("blank line between declarations should not resolve")
	}
}

func TestEnclosingUnparseableFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "broken.go")
	if err := os.WriteFile(p, []byte("package !!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := NewGo().Enclosing(p, 1); ok {
		t.Fatal("unparseable file must not resolve")
	}
}

func TestResolveWrapperReturnsEmptyString(t *testing.T) {
	p := writeSample(t)
	if got := Resolve(NewGo(), p, 3); got != "" {
		t.Fatalf("Resolve = %q, want empty", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd bughunt && go test ./internal/symbol/
```

Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the interface**

Create `bughunt/internal/symbol/symbol.go`:

```go
// Package symbol maps a source location to the name of its enclosing
// declaration. Fingerprints are built on that name rather than a line number,
// so a finding survives edits elsewhere in the file.
package symbol

// Resolver names the declaration enclosing a source line.
type Resolver interface {
	// Enclosing returns the declaration name containing line, and whether one
	// was found. Lines outside any declaration, and files that cannot be
	// parsed, return false.
	Enclosing(file string, line int) (string, bool)
}

// Resolve calls r.Enclosing and flattens the miss case to an empty string.
func Resolve(r Resolver, file string, line int) string {
	name, ok := r.Enclosing(file, line)
	if !ok {
		return ""
	}
	return name
}
```

- [ ] **Step 4: Write the Go resolver**

Create `bughunt/internal/symbol/golang.go`:

```go
package symbol

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sync"
)

// NewGo returns a Resolver for Go source files. Parsed files are cached for the
// lifetime of the resolver, since one scan asks about many lines per file.
func NewGo() Resolver { return &goResolver{cache: map[string]*parsedFile{}} }

type parsedFile struct {
	fset *token.FileSet
	file *ast.File
}

type goResolver struct {
	mu    sync.Mutex
	cache map[string]*parsedFile
}

func (g *goResolver) parse(path string) *parsedFile {
	g.mu.Lock()
	defer g.mu.Unlock()
	if pf, ok := g.cache[path]; ok {
		return pf
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	pf := (*parsedFile)(nil)
	if err == nil {
		pf = &parsedFile{fset: fset, file: f}
	}
	g.cache[path] = pf
	return pf
}

func (g *goResolver) Enclosing(path string, line int) (string, bool) {
	pf := g.parse(path)
	if pf == nil {
		return "", false
	}
	for _, decl := range pf.file.Decls {
		start := pf.fset.Position(decl.Pos()).Line
		end := pf.fset.Position(decl.End()).Line
		if line < start || line > end {
			continue
		}
		switch d := decl.(type) {
		case *ast.FuncDecl:
			return funcName(d), true
		case *ast.GenDecl:
			if name, ok := genDeclName(pf, d, line); ok {
				return name, true
			}
		}
	}
	return "", false
}

// funcName renders a function as "Name" and a method as "Receiver.Name",
// ignoring pointer-ness so *T and T methods share a namespace.
func funcName(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name
	}
	return receiverTypeName(d.Recv.List[0].Type) + "." + d.Name.Name
}

func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr: // generic receiver: T[P]
		return receiverTypeName(t.X)
	case *ast.IndexListExpr: // generic receiver: T[P, Q]
		return receiverTypeName(t.X)
	default:
		return "?"
	}
}

// genDeclName finds the specific const/var/type spec covering line, so a large
// var block does not collapse every finding inside it onto one name.
func genDeclName(pf *parsedFile, d *ast.GenDecl, line int) (string, bool) {
	for _, spec := range d.Specs {
		start := pf.fset.Position(spec.Pos()).Line
		end := pf.fset.Position(spec.End()).Line
		if line < start || line > end {
			continue
		}
		switch s := spec.(type) {
		case *ast.TypeSpec:
			return s.Name.Name, true
		case *ast.ValueSpec:
			if len(s.Names) > 0 {
				return s.Names[0].Name, true
			}
		}
	}
	return "", false
}
```

- [ ] **Step 5: Run test to verify it passes**

```bash
cd bughunt && go test ./internal/symbol/
```

Expected: PASS, all six tests.

- [ ] **Step 6: Commit**

```bash
git add bughunt/internal/symbol
git commit -m "feat(bughunt): Go symbol resolution for fingerprint anchoring"
```

---

### Task 5: Fingerprints

**Files:**
- Create: `bughunt/internal/finding/hit.go`
- Create: `bughunt/internal/finding/fingerprint.go`
- Test: `bughunt/internal/finding/fingerprint_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Severity string` with `SeverityLow`, `SeverityMedium`, `SeverityHigh` (`"low"`, `"medium"`, `"high"`), and `func (s Severity) AtLeast(floor Severity) bool`.
  - `type Hit struct { RuleID, File string; Line int; Symbol, Message string; Severity Severity; MatchText, RawRef string }`
  - `func Fingerprint(h Hit) string` — 16 hex characters of SHA-256 over `ruleID \x00 anchor \x00 normalize(matchText)`, where `anchor` is `h.Symbol` when non-empty and `h.File` otherwise.
  - `func Normalize(s string) string` — collapses all whitespace runs to a single space and trims. Exported so rule authors can reason about matching.

- [ ] **Step 1: Write the failing test**

Create `bughunt/internal/finding/fingerprint_test.go`:

```go
package finding

import "testing"

func base() Hit {
	return Hit{RuleID: "govet/printf", File: "internal/scan/scan.go", Line: 42,
		Symbol: "Run", MatchText: "fmt.Printf(\"%d\", s)", Severity: SeverityMedium}
}

func TestFingerprintIsStableAcrossLineMoves(t *testing.T) {
	a := base()
	b := base()
	b.Line = 998
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("line number must not affect the fingerprint")
	}
}

func TestFingerprintIsStableAcrossReformatting(t *testing.T) {
	a := base()
	b := base()
	b.MatchText = "fmt.Printf(\"%d\",\n\t\ts)"
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("whitespace-only differences must not affect the fingerprint")
	}
}

func TestFingerprintIsStableAcrossFileMoves(t *testing.T) {
	a := base()
	b := base()
	b.File = "internal/scanner/scan.go"
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("a symbol moved to another file keeps its fingerprint")
	}
}

func TestFingerprintChangesWhenSymbolRenamed(t *testing.T) {
	a := base()
	b := base()
	b.Symbol = "Execute"
	if Fingerprint(a) == Fingerprint(b) {
		t.Fatal("renaming the enclosing symbol must produce a new fingerprint")
	}
}

func TestFingerprintChangesWithRuleAndMatch(t *testing.T) {
	a := base()
	byRule := base()
	byRule.RuleID = "gosec/G404"
	if Fingerprint(a) == Fingerprint(byRule) {
		t.Fatal("different rules must not collide")
	}
	byMatch := base()
	byMatch.MatchText = "fmt.Printf(\"%s\", s)"
	if Fingerprint(a) == Fingerprint(byMatch) {
		t.Fatal("different matched code must not collide")
	}
}

func TestFingerprintFallsBackToFileWithoutSymbol(t *testing.T) {
	a := base()
	a.Symbol = ""
	b := a
	b.File = "other.go"
	if Fingerprint(a) == Fingerprint(b) {
		t.Fatal("without a symbol, the file path anchors the fingerprint")
	}
}

func TestFingerprintLength(t *testing.T) {
	if got := len(Fingerprint(base())); got != 16 {
		t.Fatalf("length = %d, want 16", got)
	}
}

func TestNormalize(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"  a   b \n\t c ", "a b c"},
		{"a\n\nb", "a b"},
		{"", ""},
	} {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSeverityAtLeast(t *testing.T) {
	if !SeverityHigh.AtLeast(SeverityLow) {
		t.Error("high >= low")
	}
	if SeverityLow.AtLeast(SeverityMedium) {
		t.Error("low < medium")
	}
	if !SeverityMedium.AtLeast(SeverityMedium) {
		t.Error("medium >= medium")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd bughunt && go test ./internal/finding/
```

Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the Hit type**

Create `bughunt/internal/finding/hit.go`:

```go
// Package finding defines the normalized output of every detector and the
// identity function that deduplicates it across runs.
package finding

// Severity ranks a hit. Detector-native severities are mapped onto these three.
type Severity string

const (
	SeverityLow    Severity = "low"
	SeverityMedium Severity = "medium"
	SeverityHigh   Severity = "high"
)

var severityRank = map[Severity]int{SeverityLow: 0, SeverityMedium: 1, SeverityHigh: 2}

// AtLeast reports whether s ranks at or above floor. An unknown severity is
// treated as low, so a detector emitting something unexpected is filtered
// rather than promoted.
func (s Severity) AtLeast(floor Severity) bool {
	return severityRank[s] >= severityRank[floor]
}

// Hit is one detector result, normalized across tools.
type Hit struct {
	// RuleID is namespaced by detector, e.g. "govet/printf" or "gosec/G404".
	RuleID string
	File   string
	// Line is display data only and never enters the fingerprint.
	Line     int
	Symbol   string
	Message  string
	Severity Severity
	// MatchText is the source text the detector flagged. It participates in
	// identity, so it must come from the source, not from the tool's prose.
	MatchText string
	// RawRef points at retained raw detector output (an agentsh invocation id),
	// empty when nothing was retained.
	RawRef string
}
```

- [ ] **Step 4: Write the fingerprint function**

Create `bughunt/internal/finding/fingerprint.go`:

```go
package finding

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Fingerprint is the stable identity of a hit: the rule that fired, the
// declaration that contains it, and the code that matched. Line numbers and
// file paths are deliberately excluded so a finding survives edits above it and
// travels with a function moved to another file. A renamed enclosing symbol
// yields a new fingerprint, which is intended — renamed code deserves a fresh
// look.
//
// When no enclosing symbol could be resolved, the file path anchors the
// fingerprint instead. That is weaker, but it is the only stable handle left.
func Fingerprint(h Hit) string {
	anchor := h.Symbol
	if anchor == "" {
		anchor = h.File
	}
	sum := sha256.Sum256([]byte(h.RuleID + "\x00" + anchor + "\x00" + Normalize(h.MatchText)))
	return hex.EncodeToString(sum[:])[:16]
}

// Normalize collapses whitespace runs to single spaces and trims the result, so
// reformatting does not change identity.
func Normalize(s string) string { return strings.Join(strings.Fields(s), " ") }
```

- [ ] **Step 5: Run test to verify it passes**

```bash
cd bughunt && go test ./internal/finding/
```

Expected: PASS, all nine tests.

- [ ] **Step 6: Commit**

```bash
git add bughunt/internal/finding
git commit -m "feat(bughunt): stable finding fingerprints"
```

---

### Task 6: Detector interface, registry, and `go vet`

**Files:**
- Create: `bughunt/internal/detector/detector.go`
- Create: `bughunt/internal/detector/exec.go`
- Create: `bughunt/internal/detector/govet.go`
- Test: `bughunt/internal/detector/govet_test.go`
- Test: `bughunt/internal/detector/detector_test.go`

**Interfaces:**
- Consumes: `finding.Hit`, `finding.Severity`, `symbol.Resolver`.
- Produces:
  - `type Runner interface { Run(ctx context.Context, name string, args []string, dir string) (stdout, stderr string, exitCode int, rawRef string, err error) }`
  - `func NewExecRunner() Runner` — plain `os/exec`, `rawRef` always empty. Plan 3 adds an agentsh-backed runner behind the same interface.
  - `type Detector interface { Name() string; Available(ctx context.Context) bool; Run(ctx context.Context, dir string, paths []string) ([]finding.Hit, error) }`
  - `type Registry struct{ ... }` with `func NewRegistry(dets ...Detector) *Registry`, `func (r *Registry) Enabled(cfg interface{ DetectorEnabled(string) bool }) []Detector`, and `func (r *Registry) Partition(ctx context.Context, dets []Detector) (available, unavailable []Detector)`.
  - `func NewGoVet(run Runner, sym symbol.Resolver) Detector`

- [ ] **Step 1: Write the failing tests**

Create `bughunt/internal/detector/detector_test.go`:

```go
package detector

import (
	"context"
	"testing"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
)

type fakeDetector struct {
	name      string
	available bool
}

func (f fakeDetector) Name() string                          { return f.name }
func (f fakeDetector) Available(context.Context) bool         { return f.available }
func (f fakeDetector) Run(context.Context, string, []string) ([]finding.Hit, error) {
	return nil, nil
}

type fakeConfig map[string]bool

func (c fakeConfig) DetectorEnabled(name string) bool {
	enabled, ok := c[name]
	return !ok || enabled
}

func TestRegistryEnabledRespectsConfig(t *testing.T) {
	r := NewRegistry(fakeDetector{name: "govet"}, fakeDetector{name: "gosec"})
	got := r.Enabled(fakeConfig{"gosec": false})
	if len(got) != 1 || got[0].Name() != "govet" {
		t.Fatalf("Enabled returned %v, want [govet]", names(got))
	}
}

func TestRegistryPartitionSplitsOnAvailability(t *testing.T) {
	r := NewRegistry()
	dets := []Detector{fakeDetector{name: "here", available: true}, fakeDetector{name: "gone"}}
	available, unavailable := r.Partition(context.Background(), dets)
	if len(available) != 1 || available[0].Name() != "here" {
		t.Fatalf("available = %v, want [here]", names(available))
	}
	if len(unavailable) != 1 || unavailable[0].Name() != "gone" {
		t.Fatalf("unavailable = %v, want [gone]", names(unavailable))
	}
}

func names(dets []Detector) []string {
	out := make([]string, len(dets))
	for i, d := range dets {
		out[i] = d.Name()
	}
	return out
}
```

Create `bughunt/internal/detector/govet_test.go`:

```go
package detector

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// fakeRunner replays captured tool output so parsing is tested without the tool.
type fakeRunner struct {
	stdout, stderr string
	exitCode       int
}

func (f fakeRunner) Run(context.Context, string, []string, string) (string, string, int, string, error) {
	return f.stdout, f.stderr, f.exitCode, "", nil
}

const vetSource = `package sample

import "fmt"

func Alpha(s string) {
	fmt.Printf("%d", s)
}
`

// go vet writes diagnostics to stderr as "file:line:col: message".
func TestGoVetParsesDiagnostics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte(vetSource), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr := "# sample\n" + path + ":6:2: fmt.Printf format %d has arg s of wrong type string\n"

	d := NewGoVet(fakeRunner{stderr: stderr, exitCode: 1}, symbol.NewGo())
	hits, err := d.Run(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	h := hits[0]
	if h.RuleID != "govet/printf" {
		t.Errorf("RuleID = %q, want govet/printf", h.RuleID)
	}
	if h.Line != 6 {
		t.Errorf("Line = %d, want 6", h.Line)
	}
	if h.Symbol != "Alpha" {
		t.Errorf("Symbol = %q, want Alpha — hits must be anchored to a declaration", h.Symbol)
	}
	if h.MatchText != `fmt.Printf("%d", s)` {
		t.Errorf("MatchText = %q, want the flagged source line", h.MatchText)
	}
	if h.Severity != finding.SeverityMedium {
		t.Errorf("Severity = %q, want medium", h.Severity)
	}
}

func TestGoVetIgnoresNonDiagnosticLines(t *testing.T) {
	d := NewGoVet(fakeRunner{stderr: "# sample\nsome unrelated noise\n"}, symbol.NewGo())
	hits, err := d.Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("got %d hits, want 0", len(hits))
	}
}

func TestGoVetCleanRunProducesNoHits(t *testing.T) {
	d := NewGoVet(fakeRunner{}, symbol.NewGo())
	hits, err := d.Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("got %d hits, want 0", len(hits))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd bughunt && go test ./internal/detector/
```

Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the interfaces and registry**

Create `bughunt/internal/detector/detector.go`:

```go
// Package detector adapts external analysis tools to one normalized interface.
// Adding a language means adding adapters here and nothing else.
package detector

import (
	"context"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
)

// Detector wraps one analysis tool.
type Detector interface {
	// Name is the stable identifier used in config and in rule id prefixes.
	Name() string
	// Available reports whether the underlying tool is installed and usable.
	Available(ctx context.Context) bool
	// Run analyses paths within dir. Empty paths means the whole tree.
	Run(ctx context.Context, dir string, paths []string) ([]finding.Hit, error)
}

// Registry holds the detectors compiled into this binary.
type Registry struct{ dets []Detector }

// NewRegistry builds a registry over dets.
func NewRegistry(dets ...Detector) *Registry { return &Registry{dets: dets} }

// All returns every registered detector.
func (r *Registry) All() []Detector { return r.dets }

// Enabled filters the registry by configuration.
func (r *Registry) Enabled(cfg interface{ DetectorEnabled(string) bool }) []Detector {
	var out []Detector
	for _, d := range r.dets {
		if cfg.DetectorEnabled(d.Name()) {
			out = append(out, d)
		}
	}
	return out
}

// Partition splits detectors by whether their tool is installed. A missing tool
// is reported, never fatal: a gate that fails because a linter is not installed
// gets disabled by the first person it annoys.
func (r *Registry) Partition(ctx context.Context, dets []Detector) (available, unavailable []Detector) {
	for _, d := range dets {
		if d.Available(ctx) {
			available = append(available, d)
		} else {
			unavailable = append(unavailable, d)
		}
	}
	return available, unavailable
}
```

- [ ] **Step 4: Write the exec runner**

Create `bughunt/internal/detector/exec.go`:

```go
package detector

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

// Runner executes a detector process. It exists so raw output can later be
// routed through agentsh for retention without changing any adapter.
type Runner interface {
	// Run executes name with args in dir. A non-zero exit is not an error:
	// analysis tools signal findings that way. err is reserved for failures to
	// execute at all.
	Run(ctx context.Context, name string, args []string, dir string) (stdout, stderr string, exitCode int, rawRef string, err error)
}

// NewExecRunner returns a Runner backed by os/exec, retaining nothing.
func NewExecRunner() Runner { return execRunner{} }

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args []string, dir string) (string, string, int, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return stdout.String(), stderr.String(), 0, "", nil
	case errors.As(err, &exitErr):
		return stdout.String(), stderr.String(), exitErr.ExitCode(), "", nil
	default:
		return stdout.String(), stderr.String(), -1, "", err
	}
}

// lookPath reports whether a binary is on PATH.
func lookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
```

- [ ] **Step 5: Write the go vet adapter**

Create `bughunt/internal/detector/govet.go`:

```go
package detector

import (
	"bufio"
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// diagnosticLine matches the "file:line:col: message" form every Go analysis
// tool in this package emits.
var diagnosticLine = regexp.MustCompile(`^(.+?):(\d+):(\d+): (.+)$`)

// NewGoVet adapts `go vet`, which writes diagnostics to stderr.
func NewGoVet(run Runner, sym symbol.Resolver) Detector {
	return &goVet{run: run, sym: sym}
}

type goVet struct {
	run Runner
	sym symbol.Resolver
}

func (g *goVet) Name() string { return "govet" }

func (g *goVet) Available(context.Context) bool { return lookPath("go") }

func (g *goVet) Run(ctx context.Context, dir string, paths []string) ([]finding.Hit, error) {
	args := []string{"vet"}
	if len(paths) == 0 {
		args = append(args, "./...")
	} else {
		args = append(args, paths...)
	}
	_, stderr, _, rawRef, err := g.run.Run(ctx, "go", args, dir)
	if err != nil {
		return nil, err
	}
	return parseDiagnostics(stderr, g.Name(), rawRef, g.sym, vetRuleID, finding.SeverityMedium), nil
}

// vetRuleID derives a rule id from the analyzer that produced the message. go
// vet does not label its analyzers, so the first recognizable token is used;
// unrecognized messages fall back to a generic id.
func vetRuleID(message string) string {
	analyzers := []struct{ prefix, name string }{
		{"fmt.Printf", "printf"}, {"fmt.Sprintf", "printf"}, {"fmt.Errorf", "printf"},
		{"lost cancel", "lostcancel"},
		{"unreachable", "unreachable"},
		{"self-assignment", "assign"},
		{"suspect or", "bools"},
		{"struct field", "structtag"},
		{"the cancel function", "lostcancel"},
	}
	for _, a := range analyzers {
		if strings.Contains(message, a.prefix) {
			return "govet/" + a.name
		}
	}
	return "govet/vet"
}

// parseDiagnostics turns "file:line:col: message" output into hits, resolving
// the enclosing symbol and reading back the flagged source line as match text.
func parseDiagnostics(output, detectorName, rawRef string, sym symbol.Resolver,
	ruleID func(string) string, severity finding.Severity) []finding.Hit {

	var hits []finding.Hit
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		m := diagnosticLine.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		file := m[1]
		line, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		message := m[4]
		hits = append(hits, finding.Hit{
			RuleID:    ruleID(message),
			File:      file,
			Line:      line,
			Symbol:    symbol.Resolve(sym, file, line),
			Message:   message,
			Severity:  severity,
			MatchText: sourceLine(file, line),
			RawRef:    rawRef,
		})
	}
	return hits
}

// sourceLine returns the trimmed contents of a 1-indexed line, or "" if the
// file cannot be read. An unreadable file degrades identity to rule+symbol,
// which is still stable, so this is not an error.
func sourceLine(path string, line int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	if line < 1 || line > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line-1])
}
```

- [ ] **Step 6: Run tests to verify they pass**

```bash
cd bughunt && go test ./internal/detector/
```

Expected: PASS, all five tests.

- [ ] **Step 7: Commit**

```bash
git add bughunt/internal/detector
git commit -m "feat(bughunt): detector interface, registry, and go vet adapter"
```

---

### Task 7: staticcheck, errcheck, ineffassign, and gosec adapters

The first four share `parseDiagnostics` from Task 6. gosec is separate: it emits JSON.

**Files:**
- Create: `bughunt/internal/detector/golangtools.go`
- Create: `bughunt/internal/detector/gosec.go`
- Test: `bughunt/internal/detector/golangtools_test.go`
- Test: `bughunt/internal/detector/gosec_test.go`

**Interfaces:**
- Consumes: `Runner`, `symbol.Resolver`, `parseDiagnostics`, `sourceLine`, `lookPath` (all from Task 6).
- Produces: `func NewStaticcheck(run Runner, sym symbol.Resolver) Detector`, `func NewErrcheck(...) Detector`, `func NewIneffassign(...) Detector`, `func NewGosec(...) Detector`.

- [ ] **Step 1: Write the failing tests**

Create `bughunt/internal/detector/golangtools_test.go`:

```go
package detector

import (
	"context"
	"testing"

	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// staticcheck writes "file:line:col: message (SAxxxx)" to stdout.
func TestStaticcheckExtractsCheckIDFromSuffix(t *testing.T) {
	out := "/tmp/x/a.go:12:3: this value of err is never used (SA4006)\n"
	hits, err := NewStaticcheck(fakeRunner{stdout: out, exitCode: 1}, symbol.NewGo()).
		Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if hits[0].RuleID != "staticcheck/SA4006" {
		t.Fatalf("RuleID = %q, want staticcheck/SA4006", hits[0].RuleID)
	}
	if hits[0].Message != "this value of err is never used" {
		t.Fatalf("Message = %q, want the message without the check suffix", hits[0].Message)
	}
}

func TestStaticcheckWithoutCheckIDFallsBack(t *testing.T) {
	out := "/tmp/x/a.go:12:3: something happened\n"
	hits, err := NewStaticcheck(fakeRunner{stdout: out}, symbol.NewGo()).
		Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hits[0].RuleID != "staticcheck/staticcheck" {
		t.Fatalf("RuleID = %q, want staticcheck/staticcheck", hits[0].RuleID)
	}
}

func TestErrcheckParsesStdout(t *testing.T) {
	out := "/tmp/x/a.go:9:10: defer f.Close()\n"
	hits, err := NewErrcheck(fakeRunner{stdout: out, exitCode: 1}, symbol.NewGo()).
		Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].RuleID != "errcheck/unchecked" {
		t.Fatalf("hits = %+v, want one errcheck/unchecked", hits)
	}
}

func TestIneffassignParsesStdout(t *testing.T) {
	out := "/tmp/x/a.go:4:2: ineffectual assignment to x\n"
	hits, err := NewIneffassign(fakeRunner{stdout: out, exitCode: 1}, symbol.NewGo()).
		Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].RuleID != "ineffassign/ineffassign" {
		t.Fatalf("hits = %+v, want one ineffassign/ineffassign", hits)
	}
}
```

Create `bughunt/internal/detector/gosec_test.go`:

```go
package detector

import (
	"context"
	"testing"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

const gosecJSON = `{
  "Issues": [
    {
      "severity": "HIGH",
      "confidence": "HIGH",
      "rule_id": "G404",
      "details": "Use of weak random number generator",
      "file": "/tmp/x/a.go",
      "line": "17",
      "column": "9",
      "code": "rand.Intn(10)"
    }
  ]
}`

func TestGosecParsesJSON(t *testing.T) {
	hits, err := NewGosec(fakeRunner{stdout: gosecJSON, exitCode: 1}, symbol.NewGo()).
		Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	h := hits[0]
	if h.RuleID != "gosec/G404" {
		t.Errorf("RuleID = %q, want gosec/G404", h.RuleID)
	}
	if h.Line != 17 {
		t.Errorf("Line = %d, want 17", h.Line)
	}
	if h.Severity != finding.SeverityHigh {
		t.Errorf("Severity = %q, want high", h.Severity)
	}
	if h.MatchText != "rand.Intn(10)" {
		t.Errorf("MatchText = %q, want the code field", h.MatchText)
	}
}

func TestGosecEmptyOutputIsNotAnError(t *testing.T) {
	hits, err := NewGosec(fakeRunner{}, symbol.NewGo()).Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("got %d hits, want 0", len(hits))
	}
}

func TestGosecMalformedJSONIsAnError(t *testing.T) {
	_, err := NewGosec(fakeRunner{stdout: "{not json"}, symbol.NewGo()).
		Run(context.Background(), t.TempDir(), nil)
	if err == nil {
		t.Fatal("malformed detector output must surface as an error, not silence")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd bughunt && go test ./internal/detector/
```

Expected: FAIL — `undefined: NewStaticcheck`, `NewErrcheck`, `NewIneffassign`, `NewGosec`.

- [ ] **Step 3: Write the line-oriented adapters**

Create `bughunt/internal/detector/golangtools.go`:

```go
package detector

import (
	"context"
	"regexp"
	"strings"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// lineTool adapts any tool emitting "file:line:col: message" on stdout.
type lineTool struct {
	name     string
	binary   string
	args     func(paths []string) []string
	ruleID   func(message string) string
	severity finding.Severity
	run      Runner
	sym      symbol.Resolver
}

func (t *lineTool) Name() string { return t.name }

func (t *lineTool) Available(context.Context) bool { return lookPath(t.binary) }

func (t *lineTool) Run(ctx context.Context, dir string, paths []string) ([]finding.Hit, error) {
	stdout, _, _, rawRef, err := t.run.Run(ctx, t.binary, t.args(paths), dir)
	if err != nil {
		return nil, err
	}
	return parseDiagnostics(stdout, t.name, rawRef, t.sym, t.ruleID, t.severity), nil
}

func targets(paths []string) []string {
	if len(paths) == 0 {
		return []string{"./..."}
	}
	return paths
}

// staticcheckSuffix captures the check id staticcheck appends, e.g. "(SA4006)".
var staticcheckSuffix = regexp.MustCompile(`\s*\((S[A-Z]?\d{4}|ST\d{4}|QF\d{4})\)$`)

// NewStaticcheck adapts staticcheck.
func NewStaticcheck(run Runner, sym symbol.Resolver) Detector {
	return &lineTool{
		name: "staticcheck", binary: "staticcheck",
		args:     targets,
		ruleID:   staticcheckRuleID,
		severity: finding.SeverityMedium,
		run:      run, sym: sym,
	}
}

func staticcheckRuleID(message string) string {
	if m := staticcheckSuffix.FindStringSubmatch(message); m != nil {
		return "staticcheck/" + m[1]
	}
	return "staticcheck/staticcheck"
}

// NewErrcheck adapts errcheck. Unchecked errors are one class, so the rule id
// is fixed.
func NewErrcheck(run Runner, sym symbol.Resolver) Detector {
	return &lineTool{
		name: "errcheck", binary: "errcheck",
		args:     targets,
		ruleID:   func(string) string { return "errcheck/unchecked" },
		severity: finding.SeverityMedium,
		run:      run, sym: sym,
	}
}

// NewIneffassign adapts ineffassign.
func NewIneffassign(run Runner, sym symbol.Resolver) Detector {
	return &lineTool{
		name: "ineffassign", binary: "ineffassign",
		args:     targets,
		ruleID:   func(string) string { return "ineffassign/ineffassign" },
		severity: finding.SeverityLow,
		run:      run, sym: sym,
	}
}

// trimCheckSuffix strips a trailing check id from a message so the same defect
// reported with and without a suffix reads identically.
func trimCheckSuffix(message string) string {
	return strings.TrimSpace(staticcheckSuffix.ReplaceAllString(message, ""))
}
```

Now make `parseDiagnostics` strip the suffix from the stored message. In `govet.go`, change the `Message:` field assignment inside `parseDiagnostics` from `Message: message,` to `Message: trimCheckSuffix(message),`. The rule id is still derived from the untrimmed message, so ordering matters: derive first, trim second. The existing call `ruleID(message)` already runs on the untrimmed text.

- [ ] **Step 4: Write the gosec adapter**

Create `bughunt/internal/detector/gosec.go`:

```go
package detector

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// NewGosec adapts gosec, which emits JSON and reports its own severities.
func NewGosec(run Runner, sym symbol.Resolver) Detector {
	return &gosec{run: run, sym: sym}
}

type gosec struct {
	run Runner
	sym symbol.Resolver
}

func (g *gosec) Name() string { return "gosec" }

func (g *gosec) Available(context.Context) bool { return lookPath("gosec") }

type gosecReport struct {
	Issues []struct {
		Severity string `json:"severity"`
		RuleID   string `json:"rule_id"`
		Details  string `json:"details"`
		File     string `json:"file"`
		Line     string `json:"line"`
		Code     string `json:"code"`
	} `json:"Issues"`
}

func (g *gosec) Run(ctx context.Context, dir string, paths []string) ([]finding.Hit, error) {
	stdout, _, _, rawRef, err := g.run.Run(ctx, "gosec",
		append([]string{"-fmt=json", "-quiet"}, targets(paths)...), dir)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(stdout) == "" {
		return nil, nil
	}

	var report gosecReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		return nil, err
	}

	var hits []finding.Hit
	for _, issue := range report.Issues {
		// gosec reports line as a string, sometimes as a "12-14" range.
		lineText, _, _ := strings.Cut(issue.Line, "-")
		line, err := strconv.Atoi(lineText)
		if err != nil {
			continue
		}
		hits = append(hits, finding.Hit{
			RuleID:    "gosec/" + issue.RuleID,
			File:      issue.File,
			Line:      line,
			Symbol:    symbol.Resolve(g.sym, issue.File, line),
			Message:   issue.Details,
			Severity:  gosecSeverity(issue.Severity),
			MatchText: strings.TrimSpace(issue.Code),
			RawRef:    rawRef,
		})
	}
	return hits, nil
}

func gosecSeverity(s string) finding.Severity {
	switch strings.ToUpper(s) {
	case "HIGH":
		return finding.SeverityHigh
	case "MEDIUM":
		return finding.SeverityMedium
	default:
		return finding.SeverityLow
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
cd bughunt && go test ./internal/detector/
```

Expected: PASS, all twelve tests in the package.

- [ ] **Step 6: Commit**

```bash
git add bughunt/internal/detector
git commit -m "feat(bughunt): staticcheck, errcheck, ineffassign, gosec adapters"
```

---

### Task 8: ast-grep adapter for repo-local rules

**Files:**
- Create: `bughunt/internal/detector/astgrep.go`
- Test: `bughunt/internal/detector/astgrep_test.go`

**Interfaces:**
- Consumes: `Runner`, `symbol.Resolver`.
- Produces: `func NewAstGrep(run Runner, sym symbol.Resolver, rulesDir string) Detector`. Name is `astgrep`; rule ids are `astgrep/<rule id from the yaml>`. Unavailable when the `ast-grep` binary is missing OR `rulesDir` holds no `.yaml` files — running a rule engine with no rules is noise in the run record.

- [ ] **Step 1: Write the failing test**

Create `bughunt/internal/detector/astgrep_test.go`:

```go
package detector

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// ast-grep --json emits an array of matches.
const astGrepJSON = `[
  {
    "ruleId": "no-time-now-in-handler",
    "severity": "warning",
    "message": "call to time.Now inside a handler",
    "file": "internal/api/handler.go",
    "range": {"start": {"line": 41, "column": 8}, "end": {"line": 41, "column": 20}},
    "text": "time.Now()"
  }
]`

func rulesDirWith(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("id: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestAstGrepParsesJSONAndConvertsToOneIndexedLines(t *testing.T) {
	d := NewAstGrep(fakeRunner{stdout: astGrepJSON}, symbol.NewGo(), rulesDirWith(t, "r.yaml"))
	hits, err := d.Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	h := hits[0]
	if h.RuleID != "astgrep/no-time-now-in-handler" {
		t.Errorf("RuleID = %q", h.RuleID)
	}
	if h.Line != 42 {
		t.Errorf("Line = %d, want 42 — ast-grep lines are 0-indexed", h.Line)
	}
	if h.MatchText != "time.Now()" {
		t.Errorf("MatchText = %q, want time.Now()", h.MatchText)
	}
	if h.Severity != finding.SeverityMedium {
		t.Errorf("Severity = %q, want medium for warning", h.Severity)
	}
}

func TestAstGrepUnavailableWithoutRules(t *testing.T) {
	d := NewAstGrep(fakeRunner{}, symbol.NewGo(), rulesDirWith(t))
	if d.Available(context.Background()) {
		t.Fatal("no rule files means the adapter has nothing to do")
	}
}

func TestAstGrepUnavailableWhenRulesDirMissing(t *testing.T) {
	d := NewAstGrep(fakeRunner{}, symbol.NewGo(), filepath.Join(t.TempDir(), "absent"))
	if d.Available(context.Background()) {
		t.Fatal("missing rules dir must not be available")
	}
}

func TestAstGrepEmptyOutputIsNotAnError(t *testing.T) {
	d := NewAstGrep(fakeRunner{}, symbol.NewGo(), rulesDirWith(t, "r.yaml"))
	hits, err := d.Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("got %d hits, want 0", len(hits))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd bughunt && go test ./internal/detector/
```

Expected: FAIL — `undefined: NewAstGrep`.

- [ ] **Step 3: Write the implementation**

Create `bughunt/internal/detector/astgrep.go`:

```go
package detector

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// NewAstGrep runs the repo-local rules under rulesDir. This is the detector
// that emitted rules feed into, so it is the one that grows as the tool learns.
func NewAstGrep(run Runner, sym symbol.Resolver, rulesDir string) Detector {
	return &astGrep{run: run, sym: sym, rulesDir: rulesDir}
}

type astGrep struct {
	run      Runner
	sym      symbol.Resolver
	rulesDir string
}

func (a *astGrep) Name() string { return "astgrep" }

// Available requires both the binary and at least one rule. Reporting the
// engine as available with no rules would record a detector that cannot
// possibly find anything.
func (a *astGrep) Available(context.Context) bool {
	if !lookPath("ast-grep") {
		return false
	}
	matches, err := filepath.Glob(filepath.Join(a.rulesDir, "*.yaml"))
	return err == nil && len(matches) > 0
}

type astGrepMatch struct {
	RuleID   string `json:"ruleId"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	File     string `json:"file"`
	Text     string `json:"text"`
	Range    struct {
		Start struct {
			Line int `json:"line"`
		} `json:"start"`
	} `json:"range"`
}

func (a *astGrep) Run(ctx context.Context, dir string, paths []string) ([]finding.Hit, error) {
	args := []string{"scan", "--json", "--rule-dir", a.rulesDir}
	args = append(args, paths...)
	stdout, _, _, rawRef, err := a.run.Run(ctx, "ast-grep", args, dir)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(stdout) == "" {
		return nil, nil
	}

	var matches []astGrepMatch
	if err := json.Unmarshal([]byte(stdout), &matches); err != nil {
		return nil, err
	}

	var hits []finding.Hit
	for _, m := range matches {
		line := m.Range.Start.Line + 1 // ast-grep lines are 0-indexed
		abs := m.File
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(dir, m.File)
		}
		hits = append(hits, finding.Hit{
			RuleID:    "astgrep/" + m.RuleID,
			File:      m.File,
			Line:      line,
			Symbol:    symbol.Resolve(a.sym, abs, line),
			Message:   m.Message,
			Severity:  astGrepSeverity(m.Severity),
			MatchText: strings.TrimSpace(m.Text),
			RawRef:    rawRef,
		})
	}
	return hits, nil
}

func astGrepSeverity(s string) finding.Severity {
	switch strings.ToLower(s) {
	case "error":
		return finding.SeverityHigh
	case "warning":
		return finding.SeverityMedium
	default:
		return finding.SeverityLow
	}
}
```

Note there is no `os` import: `filepath.Glob` on a missing directory returns no matches and no error, which is exactly the "unavailable" answer `Available` needs.

- [ ] **Step 4: Run test to verify it passes**

```bash
cd bughunt && go test ./internal/detector/ && cd .. && just lint
```

Expected: PASS, sixteen tests; lint clean.

- [ ] **Step 5: Commit**

```bash
git add bughunt/internal/detector
git commit -m "feat(bughunt): ast-grep adapter for repo-local rules"
```

---

### Task 9: `scan` orchestration

**Files:**
- Create: `bughunt/internal/scan/scan.go`
- Test: `bughunt/internal/scan/scan_test.go`

**Interfaces:**
- Consumes: `config.Config`, `store.Store`, `detector.Detector`, `detector.Registry`, `finding.Hit`, `finding.Fingerprint`.
- Produces:
  - `type Options struct { Dir string; CommitSHA string; DiffBase string; ChangedLines map[string][]int; Paths []string }`
  - `type Result struct { RunID string; Findings []Recorded; DetectorsUsed, DetectorsUnavailable []string; GatingCount int }`
  - `type Recorded struct { Hit finding.Hit; Fingerprint string; Status store.Status; Gates bool }`
  - `func Run(ctx context.Context, s *store.Store, reg *detector.Registry, cfg config.Config, opts Options) (Result, error)`
  - `func (r Result) ExitCode() int` — 1 when `GatingCount > 0`, else 0.

- [ ] **Step 1: Write the failing test**

Create `bughunt/internal/scan/scan_test.go`:

```go
package scan

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Fewbytes/sweatshop/bughunt/internal/config"
	"github.com/Fewbytes/sweatshop/bughunt/internal/detector"
	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/store"
)

type stubDetector struct {
	name      string
	available bool
	hits      []finding.Hit
}

func (s stubDetector) Name() string                   { return s.name }
func (s stubDetector) Available(context.Context) bool  { return s.available }
func (s stubDetector) Run(context.Context, string, []string) ([]finding.Hit, error) {
	return s.hits, nil
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func hit(rule, sym, text string, line int, sev finding.Severity) finding.Hit {
	return finding.Hit{RuleID: rule, File: "a.go", Line: line, Symbol: sym,
		MatchText: text, Severity: sev, Message: "m"}
}

func TestRunRecordsFindingsAndGates(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	reg := detector.NewRegistry(stubDetector{name: "govet", available: true,
		hits: []finding.Hit{hit("govet/printf", "Alpha", "x", 3, finding.SeverityMedium)}})

	res, err := Run(ctx, s, reg, config.Default(), Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(res.Findings))
	}
	if res.GatingCount != 1 || res.ExitCode() != 1 {
		t.Fatalf("GatingCount = %d, ExitCode = %d; want 1, 1", res.GatingCount, res.ExitCode())
	}
	if res.Findings[0].Status != store.StatusNew {
		t.Fatalf("status = %q, want new", res.Findings[0].Status)
	}
}

func TestRunDoesNotGateOnSuppressedFinding(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	h := hit("govet/printf", "Alpha", "x", 3, finding.SeverityMedium)
	reg := detector.NewRegistry(stubDetector{name: "govet", available: true, hits: []finding.Hit{h}})
	cfg := config.Default()

	first, err := Run(ctx, s, reg, cfg, Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	fp := first.Findings[0].Fingerprint
	if err := s.SetStatus(ctx, fp, store.StatusSuppressed, "known FP"); err != nil {
		t.Fatal(err)
	}

	second, err := Run(ctx, s, reg, cfg, Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if second.GatingCount != 0 || second.ExitCode() != 0 {
		t.Fatalf("suppressed finding still gates: count = %d, exit = %d", second.GatingCount, second.ExitCode())
	}
	if len(second.Findings) != 1 {
		t.Fatal("a suppressed finding is still reported, it just does not gate")
	}
}

func TestRunAppliesSeverityFloor(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	reg := detector.NewRegistry(stubDetector{name: "govet", available: true, hits: []finding.Hit{
		hit("govet/a", "Alpha", "x", 3, finding.SeverityLow),
		hit("govet/b", "Beta", "y", 9, finding.SeverityHigh),
	}})
	cfg := config.Default()
	cfg.SeverityFloor = "high"

	res, err := Run(ctx, s, reg, cfg, Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 1 || res.Findings[0].Hit.RuleID != "govet/b" {
		t.Fatalf("severity floor not applied: %+v", res.Findings)
	}
}

func TestRunAppliesExcludes(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	excluded := hit("govet/a", "Alpha", "x", 3, finding.SeverityHigh)
	excluded.File = "vendor/dep/a.go"
	reg := detector.NewRegistry(stubDetector{name: "govet", available: true,
		hits: []finding.Hit{excluded}})
	cfg := config.Default()
	cfg.Exclude = []string{"vendor/**"}

	res, err := Run(ctx, s, reg, cfg, Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("excluded path produced findings: %+v", res.Findings)
	}
}

func TestRunRecordsUnavailableDetectors(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	reg := detector.NewRegistry(
		stubDetector{name: "govet", available: true},
		stubDetector{name: "gosec"},
	)
	res, err := Run(ctx, s, reg, config.Default(), Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("a missing detector must not fail the scan: %v", err)
	}
	if len(res.DetectorsUsed) != 1 || res.DetectorsUsed[0] != "govet" {
		t.Errorf("DetectorsUsed = %v", res.DetectorsUsed)
	}
	if len(res.DetectorsUnavailable) != 1 || res.DetectorsUnavailable[0] != "gosec" {
		t.Errorf("DetectorsUnavailable = %v", res.DetectorsUnavailable)
	}
}

func TestRunDiffModeFiltersGatingOnly(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	inDiff := hit("govet/a", "Alpha", "x", 10, finding.SeverityHigh)
	outOfDiff := hit("govet/b", "Beta", "y", 99, finding.SeverityHigh)
	reg := detector.NewRegistry(stubDetector{name: "govet", available: true,
		hits: []finding.Hit{inDiff, outOfDiff}})

	res, err := Run(ctx, s, reg, config.Default(), Options{
		Dir:          t.TempDir(),
		DiffBase:     "HEAD",
		ChangedLines: map[string][]int{"a.go": {10}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("diff mode must still record everything, got %d", len(res.Findings))
	}
	if res.GatingCount != 1 {
		t.Fatalf("GatingCount = %d, want 1 — only the finding on a changed line gates", res.GatingCount)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd bughunt && go test ./internal/scan/
```

Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the implementation**

Create `bughunt/internal/scan/scan.go`:

```go
// Package scan runs the deterministic detectors and records what they find.
// It never calls an LLM: scan is the verb that gates commits, so it must be
// reproducible and bounded.
package scan

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Fewbytes/sweatshop/bughunt/internal/config"
	"github.com/Fewbytes/sweatshop/bughunt/internal/detector"
	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/store"
)

// Options configures one scan.
type Options struct {
	// Dir is the repository root.
	Dir string
	// CommitSHA is recorded on the run, for correlation.
	CommitSHA string
	// DiffBase is the revision --diff was given, empty in full mode.
	DiffBase string
	// ChangedLines maps repo-relative paths to changed line numbers. Only
	// consulted when DiffBase is set. Findings outside it are still recorded;
	// they simply do not gate.
	ChangedLines map[string][]int
	// Paths narrows the analysis, empty means the whole tree.
	Paths []string
}

// Recorded is one hit after fingerprinting and persistence.
type Recorded struct {
	Hit         finding.Hit
	Fingerprint string
	Status      store.Status
	// Gates is whether this finding contributes to a non-zero exit.
	Gates bool
}

// Result is the outcome of one scan.
type Result struct {
	RunID                string
	Findings             []Recorded
	DetectorsUsed        []string
	DetectorsUnavailable []string
	GatingCount          int
}

// ExitCode is 1 when anything gates, 0 otherwise.
func (r Result) ExitCode() int {
	if r.GatingCount > 0 {
		return 1
	}
	return 0
}

// Run executes every enabled and available detector, records the results, and
// reports what gates.
func Run(ctx context.Context, s *store.Store, reg *detector.Registry,
	cfg config.Config, opts Options) (Result, error) {

	runID, err := newRunID()
	if err != nil {
		return Result{}, err
	}
	res := Result{RunID: runID}

	enabled := reg.Enabled(cfg)
	available, unavailable := reg.Partition(ctx, enabled)
	for _, d := range available {
		res.DetectorsUsed = append(res.DetectorsUsed, d.Name())
	}
	for _, d := range unavailable {
		res.DetectorsUnavailable = append(res.DetectorsUnavailable, d.Name())
	}

	floor := finding.Severity(cfg.SeverityFloor)
	for _, d := range available {
		hits, err := d.Run(ctx, opts.Dir, opts.Paths)
		if err != nil {
			return Result{}, fmt.Errorf("detector %s: %w", d.Name(), err)
		}
		for _, h := range hits {
			if !h.Severity.AtLeast(floor) || cfg.Excluded(h.File) {
				continue
			}
			rec, err := record(ctx, s, runID, h, opts)
			if err != nil {
				return Result{}, err
			}
			res.Findings = append(res.Findings, rec)
			if rec.Gates {
				res.GatingCount++
			}
		}
	}

	err = s.InsertRun(ctx, store.Run{
		ID:                   runID,
		StartedAt:            time.Now().UTC().Format(time.RFC3339),
		CommitSHA:            opts.CommitSHA,
		Mode:                 "scan",
		DiffBase:             opts.DiffBase,
		DetectorsUsed:        res.DetectorsUsed,
		DetectorsUnavailable: res.DetectorsUnavailable,
		FindingCount:         len(res.Findings),
		NewCount:             res.GatingCount,
	})
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

func record(ctx context.Context, s *store.Store, runID string,
	h finding.Hit, opts Options) (Recorded, error) {

	fp := finding.Fingerprint(h)
	status, err := s.UpsertFinding(ctx, store.Finding{
		Fingerprint:  fp,
		RuleID:       h.RuleID,
		Detector:     detectorOf(h.RuleID),
		File:         h.File,
		Line:         h.Line,
		Symbol:       h.Symbol,
		Message:      h.Message,
		Severity:     string(h.Severity),
		Confidence:   1,
		FirstSeenRun: runID,
		LastSeenRun:  runID,
	})
	if err != nil {
		return Recorded{}, err
	}
	return Recorded{
		Hit:         h,
		Fingerprint: fp,
		Status:      status,
		Gates:       status.Gates() && inDiff(h, opts),
	}, nil
}

// inDiff reports whether a hit is inside the changed lines. In full mode every
// hit qualifies. Diff mode is a filter on gating only — the database still
// learns about findings outside the diff.
func inDiff(h finding.Hit, opts Options) bool {
	if opts.DiffBase == "" {
		return true
	}
	for _, line := range opts.ChangedLines[h.File] {
		if line == h.Line {
			return true
		}
	}
	return false
}

// detectorOf recovers the detector name from a namespaced rule id.
func detectorOf(ruleID string) string {
	for i := 0; i < len(ruleID); i++ {
		if ruleID[i] == '/' {
			return ruleID[:i]
		}
	}
	return ruleID
}

func newRunID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd bughunt && go test ./internal/scan/
```

Expected: PASS, all six tests.

- [ ] **Step 5: Commit**

```bash
git add bughunt/internal/scan
git commit -m "feat(bughunt): scan orchestration with gate and diff semantics"
```

---

### Task 10: git integration for diff mode

**Files:**
- Create: `bughunt/internal/gitinfo/gitinfo.go`
- Test: `bughunt/internal/gitinfo/gitinfo_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `func HeadSHA(dir string) (string, error)`
  - `func ChangedLines(dir, base string) (map[string][]int, error)` — parses `git diff --unified=0 <base>` and returns added/modified line numbers per repo-relative path.
  - `func ParseUnifiedDiff(diff string) map[string][]int` — exported so it can be tested without a repository.

- [ ] **Step 1: Write the failing test**

Create `bughunt/internal/gitinfo/gitinfo_test.go`:

```go
package gitinfo

import (
	"reflect"
	"testing"
)

const diff = `diff --git a/internal/scan/scan.go b/internal/scan/scan.go
index 1111111..2222222 100644
--- a/internal/scan/scan.go
+++ b/internal/scan/scan.go
@@ -10,0 +11,2 @@ func Run() {
+	added := 1
+	_ = added
@@ -40 +42 @@ func Other() {
-	old()
+	replaced()
diff --git a/deleted.go b/deleted.go
deleted file mode 100644
--- a/deleted.go
+++ /dev/null
@@ -1,3 +0,0 @@
-package x
-
-func Gone() {}
`

func TestParseUnifiedDiff(t *testing.T) {
	got := ParseUnifiedDiff(diff)
	want := map[string][]int{"internal/scan/scan.go": {11, 12, 42}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseUnifiedDiff = %v, want %v", got, want)
	}
}

func TestParseUnifiedDiffIgnoresDeletedFiles(t *testing.T) {
	got := ParseUnifiedDiff(diff)
	if _, ok := got["deleted.go"]; ok {
		t.Fatal("a deleted file has no changed lines to gate on")
	}
}

func TestParseUnifiedDiffEmptyInput(t *testing.T) {
	if got := ParseUnifiedDiff(""); len(got) != 0 {
		t.Fatalf("ParseUnifiedDiff(\"\") = %v, want empty", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd bughunt && go test ./internal/gitinfo/
```

Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the implementation**

Create `bughunt/internal/gitinfo/gitinfo.go`:

```go
// Package gitinfo reads the repository facts scan needs: the current commit and
// which lines a diff touched.
package gitinfo

import (
	"bufio"
	"bytes"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// HeadSHA returns the full SHA of HEAD.
func HeadSHA(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ChangedLines returns the lines added or modified relative to base, keyed by
// repo-relative path. --unified=0 keeps the hunks tight so unchanged context
// does not widen the gate.
func ChangedLines(dir, base string) (map[string][]int, error) {
	cmd := exec.Command("git", "diff", "--unified=0", base)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return ParseUnifiedDiff(out.String()), nil
}

var (
	newFileLine = regexp.MustCompile(`^\+\+\+ b/(.+)$`)
	hunkHeader  = regexp.MustCompile(`^@@ -\S+ \+(\d+)(?:,(\d+))? @@`)
)

// ParseUnifiedDiff extracts changed line numbers per file from unified diff
// text. Deleted files are skipped: they have no lines left to gate on.
func ParseUnifiedDiff(diff string) map[string][]int {
	changed := map[string][]int{}
	var current string

	scanner := bufio.NewScanner(strings.NewReader(diff))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if m := newFileLine.FindStringSubmatch(line); m != nil {
			current = m[1]
			continue
		}
		if strings.HasPrefix(line, "+++ /dev/null") {
			current = ""
			continue
		}
		m := hunkHeader.FindStringSubmatch(line)
		if m == nil || current == "" {
			continue
		}
		start, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		count := 1
		if m[2] != "" {
			count, err = strconv.Atoi(m[2])
			if err != nil {
				continue
			}
		}
		for i := 0; i < count; i++ {
			changed[current] = append(changed[current], start+i)
		}
	}
	return changed
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd bughunt && go test ./internal/gitinfo/
```

Expected: PASS, all three tests.

- [ ] **Step 5: Commit**

```bash
git add bughunt/internal/gitinfo
git commit -m "feat(bughunt): git head and changed-line extraction"
```

---

### Task 11: `bughunt init` and `bughunt scan` commands

**Files:**
- Create: `bughunt/internal/cli/paths.go`
- Create: `bughunt/internal/cli/init.go`
- Create: `bughunt/internal/cli/scan.go`
- Modify: `bughunt/cmd/bughunt/main.go`
- Test: `bughunt/internal/cli/init_test.go`
- Test: `bughunt/internal/cli/scan_test.go`

**Interfaces:**
- Consumes: everything above.
- Produces:
  - `const Dir = ".bughunt"`
  - `type Layout struct{ Dir, Config, Rules, Lessons, DB string }` and `func Paths(root string) Layout`
  - `func Registry(root string) *detector.Registry`
  - `func Init(root string, out io.Writer) error`
  - `func Scan(ctx context.Context, root string, args []string, out, errOut io.Writer) (int, error)`

- [ ] **Step 1: Write the failing tests**

Create `bughunt/internal/cli/init_test.go`:

```go
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestInitCreatesLayout(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := Init(root, &out); err != nil {
		t.Fatalf("Init: %v", err)
	}
	l := Paths(root)
	for _, dir := range []string{l.Dir, l.Rules, l.Lessons} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Errorf("%s is not a directory: %v", dir, err)
		}
	}
	if _, err := os.Stat(l.Config); err != nil {
		t.Errorf("config.yaml not written: %v", err)
	}
	if _, err := os.Stat(l.DB); err != nil {
		t.Errorf("database not created: %v", err)
	}
}

func TestInitIsIdempotent(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := Init(root, &out); err != nil {
		t.Fatal(err)
	}
	custom := []byte("severity_floor: high\n")
	if err := os.WriteFile(Paths(root).Config, custom, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Init(root, &out); err != nil {
		t.Fatalf("second Init: %v", err)
	}
	got, err := os.ReadFile(Paths(root).Config)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(custom) {
		t.Fatal("re-running init must not overwrite an existing config")
	}
}

func TestInitReportsDetectorAvailability(t *testing.T) {
	var out bytes.Buffer
	if err := Init(t.TempDir(), &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("govet")) {
		t.Fatalf("init should report on each detector, got:\n%s", out.String())
	}
}

func TestPathsLayout(t *testing.T) {
	l := Paths("/repo")
	if l.Dir != filepath.Join("/repo", ".bughunt") {
		t.Errorf("Dir = %q", l.Dir)
	}
	if l.Rules != filepath.Join("/repo", ".bughunt", "rules") {
		t.Errorf("Rules = %q", l.Rules)
	}
	if l.Lessons != filepath.Join("/repo", ".bughunt", "lessons") {
		t.Errorf("Lessons = %q", l.Lessons)
	}
}
```

Create `bughunt/internal/cli/scan_test.go`:

```go
package cli

import (
	"bytes"
	"context"
	"testing"
)

func TestScanOnCleanTreeExitsZero(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := Init(root, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()

	// No Go files and no rules: every detector either finds nothing or is
	// unavailable, so the gate must stay open.
	code, err := Scan(context.Background(), root, nil, &out, &out)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; output:\n%s", code, out.String())
	}
}

func TestScanWithoutInitIsAUsageError(t *testing.T) {
	var out bytes.Buffer
	code, err := Scan(context.Background(), t.TempDir(), nil, &out, &out)
	if err == nil {
		t.Fatal("scanning an uninitialized repo must report an error")
	}
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestScanRejectsUnknownFlag(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := Init(root, &out); err != nil {
		t.Fatal(err)
	}
	code, _ := Scan(context.Background(), root, []string{"--nope"}, &out, &out)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd bughunt && go test ./internal/cli/
```

Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the layout helper**

Create `bughunt/internal/cli/paths.go`:

```go
// Package cli implements the bughunt commands.
package cli

import "path/filepath"

// Dir is the per-repository state directory.
const Dir = ".bughunt"

// Layout names every path bughunt owns inside a repository.
type Layout struct {
	Dir     string
	Config  string
	Rules   string
	Lessons string
	DB      string
}

// Paths derives the layout for a repository root.
func Paths(root string) Layout {
	base := filepath.Join(root, Dir)
	return Layout{
		Dir:     base,
		Config:  filepath.Join(base, "config.yaml"),
		Rules:   filepath.Join(base, "rules"),
		Lessons: filepath.Join(base, "lessons"),
		DB:      filepath.Join(base, "bughunt.db"),
	}
}
```

- [ ] **Step 4: Write init**

Create `bughunt/internal/cli/init.go`:

```go
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/Fewbytes/sweatshop/bughunt/internal/store"
)

const defaultConfig = `# bughunt configuration. Every field is optional.
#
# detectors:      set a detector to false to disable it; unlisted means enabled
# severity_floor: low | medium | high — drops hits below this rank
# exclude:        glob patterns matched against repo-relative paths

detectors: {}
severity_floor: low
exclude:
  - vendor/**
`

// Init creates the .bughunt directory, database, and default config, then
// reports which detectors are installed. Re-running it never overwrites an
// existing config.
func Init(root string, out io.Writer) error {
	l := Paths(root)
	for _, dir := range []string{l.Dir, l.Rules, l.Lessons} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	if _, err := os.Stat(l.Config); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(l.Config, []byte(defaultConfig), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "wrote %s\n", l.Config)
	} else if err != nil {
		return err
	} else {
		fmt.Fprintf(out, "kept existing %s\n", l.Config)
	}

	s, err := store.Open(l.DB)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Migrate(context.Background()); err != nil {
		return err
	}
	fmt.Fprintf(out, "database ready at %s\n", l.DB)

	reportDetectors(root, out)
	return nil
}

// reportDetectors prints installed and missing tools with install hints, so a
// first run tells the user exactly what is degraded and how to fix it.
func reportDetectors(root string, out io.Writer) {
	ctx := context.Background()
	reg := Registry(root)
	available, unavailable := reg.Partition(ctx, reg.All())

	fmt.Fprintln(out, "\ndetectors available:")
	for _, d := range available {
		fmt.Fprintf(out, "  %s\n", d.Name())
	}
	if len(unavailable) == 0 {
		return
	}
	fmt.Fprintln(out, "\ndetectors missing (scans will run without them):")
	for _, d := range unavailable {
		fmt.Fprintf(out, "  %-12s %s\n", d.Name(), installHint[d.Name()])
	}
}

var installHint = map[string]string{
	"govet":       "install the Go toolchain",
	"staticcheck": "go install honnef.co/go/tools/cmd/staticcheck@latest",
	"errcheck":    "go install github.com/kisielk/errcheck@latest",
	"ineffassign": "go install github.com/gordonklaus/ineffassign@latest",
	"gosec":       "go install github.com/securego/gosec/v2/cmd/gosec@latest",
	"astgrep":     "install ast-grep, then add rules under .bughunt/rules/",
}
```

- [ ] **Step 5: Write scan**

Create `bughunt/internal/cli/scan.go`:

```go
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/Fewbytes/sweatshop/bughunt/internal/config"
	"github.com/Fewbytes/sweatshop/bughunt/internal/detector"
	"github.com/Fewbytes/sweatshop/bughunt/internal/gitinfo"
	"github.com/Fewbytes/sweatshop/bughunt/internal/scan"
	"github.com/Fewbytes/sweatshop/bughunt/internal/store"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// Registry builds the detector set for a repository. Every detector shares one
// symbol resolver so each file is parsed once per scan.
func Registry(root string) *detector.Registry {
	run := detector.NewExecRunner()
	sym := symbol.NewGo()
	return detector.NewRegistry(
		detector.NewGoVet(run, sym),
		detector.NewStaticcheck(run, sym),
		detector.NewErrcheck(run, sym),
		detector.NewIneffassign(run, sym),
		detector.NewGosec(run, sym),
		detector.NewAstGrep(run, sym, Paths(root).Rules),
	)
}

// Scan runs the deterministic detectors and returns the process exit code.
func Scan(ctx context.Context, root string, args []string, out, errOut io.Writer) (int, error) {
	// Named flags rather than fs, since io/fs is imported here.
	flags := flag.NewFlagSet("scan", flag.ContinueOnError)
	flags.SetOutput(errOut)
	diffBase := flags.String("diff", "", "gate only on findings touching lines changed since this revision")
	if err := flags.Parse(args); err != nil {
		return 2, err
	}

	l := Paths(root)
	if _, err := os.Stat(l.Dir); errors.Is(err, fs.ErrNotExist) {
		return 2, fmt.Errorf("no %s directory — run `bughunt init` first", Dir)
	} else if err != nil {
		return 3, err
	}

	cfg, err := config.Load(l.Dir)
	if err != nil {
		return 3, err
	}

	s, err := store.Open(l.DB)
	if err != nil {
		return 3, err
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		return 3, err
	}

	opts := scan.Options{Dir: root, DiffBase: *diffBase, Paths: flags.Args()}
	// Git facts are best-effort: bughunt must work in a directory that is not
	// a repository, it just cannot offer diff mode there.
	if sha, err := gitinfo.HeadSHA(root); err == nil {
		opts.CommitSHA = sha
	}
	if *diffBase != "" {
		changed, err := gitinfo.ChangedLines(root, *diffBase)
		if err != nil {
			return 2, fmt.Errorf("git diff against %q: %w", *diffBase, err)
		}
		opts.ChangedLines = changed
	}

	res, err := scan.Run(ctx, s, Registry(root), cfg, opts)
	if err != nil {
		return 3, err
	}
	printResult(out, res)
	return res.ExitCode(), nil
}

func printResult(out io.Writer, res scan.Result) {
	for _, f := range res.Findings {
		marker := " "
		if f.Gates {
			marker = "!"
		}
		fmt.Fprintf(out, "%s %s:%d [%s] %s (%s, %s)\n",
			marker, f.Hit.File, f.Hit.Line, f.Hit.RuleID, f.Hit.Message,
			f.Status, f.Fingerprint)
	}
	if len(res.DetectorsUnavailable) > 0 {
		fmt.Fprintf(out, "\nskipped unavailable detectors: %v\n", res.DetectorsUnavailable)
	}
	fmt.Fprintf(out, "\n%d findings, %d gating (run %s)\n",
		len(res.Findings), res.GatingCount, res.RunID)
}
```

- [ ] **Step 6: Wire the commands into main**

In `bughunt/cmd/bughunt/main.go`, replace the `switch` in `run` with:

```go
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "bughunt %s (%s)\n", version.Version, version.Commit)
		return 0
	case "init":
		if err := cli.Init(".", stdout); err != nil {
			fmt.Fprintf(stderr, "init: %v\n", err)
			return 3
		}
		return 0
	case "scan":
		code, err := cli.Scan(context.Background(), ".", args[1:], stdout, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "scan: %v\n", err)
		}
		return code
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s\n", args[0], usage)
		return 2
	}
```

Add `"context"` and `"github.com/Fewbytes/sweatshop/bughunt/internal/cli"` to the imports, and extend `usage`:

```go
const usage = `usage: bughunt <command> [flags]

commands:
  init                 create .bughunt/ and report detector availability
  scan [--diff <rev>]  run deterministic detectors; exit 1 on gating findings
  version              print version and exit`
```

- [ ] **Step 7: Run tests to verify they pass**

```bash
cd bughunt && go test ./...
```

Expected: PASS across every package.

- [ ] **Step 8: Commit**

```bash
git add bughunt
git commit -m "feat(bughunt): init and scan commands"
```

---

### Task 12: Dogfood against agentsh

The first real use. This task changes no library code unless the dogfood run reveals a defect — its deliverable is a recorded scan of a real codebase and a documented result.

**Files:**
- Create: `.bughunt/config.yaml` (generated)
- Create: `design-docs/bughunt-dogfood-001.md`
- Modify: `.gitignore`

**Interfaces:**
- Consumes: the `bughunt` binary.
- Produces: a committed baseline configuration and a written first-run report.

- [ ] **Step 1: Build and initialize**

```bash
just build
./bin/bughunt init
```

Expected: `.bughunt/` created; a list of available and missing detectors.

- [ ] **Step 2: Install any missing Go detectors**

Use the install hints `init` printed. At minimum install staticcheck:

```bash
go install honnef.co/go/tools/cmd/staticcheck@latest
```

Re-run `./bin/bughunt init` and confirm the availability list changed.

- [ ] **Step 3: Run the first real scan**

```bash
./bin/bughunt scan 2>&1 | tee /tmp/bughunt-dogfood-001.txt
echo "exit: $?"
```

Expected: findings printed with fingerprints; exit 1 if anything gates. This is the first evidence that fingerprints, symbol resolution, and adapters work on real code.

- [ ] **Step 4: Verify fingerprint stability by hand**

Pick a file that produced a finding, insert a blank line near the top of it, and re-scan:

```bash
./bin/bughunt scan > /tmp/bughunt-dogfood-002.txt 2>&1 || true
diff <(grep -o '([0-9a-f]\{16\})$' /tmp/bughunt-dogfood-001.txt | sort) \
     <(grep -o '([0-9a-f]\{16\})$' /tmp/bughunt-dogfood-002.txt | sort)
```

Expected: no differences. Any fingerprint that changed from a whitespace-only edit is a bug in Task 5 — fix it there, with a regression test, before continuing. Then revert the blank line.

- [ ] **Step 5: Ignore the local database, commit the config**

Add to `.gitignore`:

```
.bughunt/bughunt.db
```

The database is local state until Dolt sync lands in Plan 2; the config, rules, and lessons are shared from day one.

- [ ] **Step 6: Write the dogfood report**

Create `design-docs/bughunt-dogfood-001.md` recording: which detectors ran, how many findings by rule id, how many appear to be genuine on inspection, how many are obvious false positives, and any bug classes that no detector caught but you know exist in the code. That last list is the input to Plan 2 — it names the rules worth writing first.

- [ ] **Step 7: Commit**

```bash
git add .bughunt/config.yaml .gitignore design-docs/bughunt-dogfood-001.md
git commit -m "chore(bughunt): dogfood baseline against agentsh"
```

---

### Task 13: Pre-commit hook and CI wiring

**Files:**
- Create: `bughunt/scripts/pre-commit`
- Modify: `justfile`
- Modify: `.github/workflows/` — the existing CI workflow file (locate it with `ls .github/workflows/`)

**Interfaces:**
- Consumes: the `bughunt` binary.
- Produces: `just bughunt-hook` (installs the hook), and a CI job that runs `bughunt scan --diff` against the merge base.

- [ ] **Step 1: Write the hook**

Create `bughunt/scripts/pre-commit`:

```bash
#!/usr/bin/env bash
# bughunt pre-commit gate. Deterministic detectors only — never `hunt`, because
# an LLM in a pre-commit hook is how the hook gets deleted.
set -euo pipefail

if ! command -v bughunt >/dev/null 2>&1; then
  echo "bughunt not on PATH, skipping scan" >&2
  exit 0
fi

if ! bughunt scan --diff HEAD; then
  echo >&2
  echo "bughunt found new issues on changed lines." >&2
  echo "Triage with: bughunt triage <fingerprint> --false-positive --reason '...'" >&2
  echo "Bypass once with: git commit --no-verify" >&2
  exit 1
fi
```

- [ ] **Step 2: Make it executable and add the install recipe**

```bash
chmod +x bughunt/scripts/pre-commit
```

Add to `justfile`:

```make
# Install the bughunt pre-commit gate into this repository
bughunt-hook:
    cp {{bughunt_dir}}/scripts/pre-commit .git/hooks/pre-commit
    chmod +x .git/hooks/pre-commit
    @echo "installed .git/hooks/pre-commit — bypass a single commit with --no-verify"
```

- [ ] **Step 3: Verify the hook behaves on a clean tree**

```bash
just build
PATH="$PWD/bin:$PATH" bash bughunt/scripts/pre-commit
echo "exit: $?"
```

Expected: exit 0 when no new findings touch changed lines.

- [ ] **Step 4: Verify the hook skips cleanly when the binary is absent**

```bash
PATH=/usr/bin:/bin bash bughunt/scripts/pre-commit
echo "exit: $?"
```

Expected: exit 0 with the "not on PATH, skipping" message. A gate that hard-fails on a missing tool gets deleted.

- [ ] **Step 5: Add the CI job**

Inspect the existing workflow first:

```bash
ls .github/workflows/ && cat .github/workflows/*.yml | head -40
```

Add a job matching the file's existing style, checking out with full history so the merge base resolves:

```yaml
  bughunt:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version: '1.24.9'
      - run: go install honnef.co/go/tools/cmd/staticcheck@latest
      - run: just build
      - run: ./bin/bughunt init
      - name: Scan changed lines
        run: ./bin/bughunt scan --diff origin/${{ github.base_ref || 'master' }}
```

- [ ] **Step 6: Run the full check suite**

```bash
just ci
```

Expected: lint, tests, build, and marketplace validation all pass.

- [ ] **Step 7: Commit**

```bash
git add bughunt/scripts justfile .github/workflows
git commit -m "feat(bughunt): pre-commit hook and CI gate"
```

---

## What Plan 1 does not cover

Deliberately left to later plans, so nothing here is mistaken for an omission:

- **Dolt.** Task 3 uses SQLite behind a driver-agnostic API. Swapping in embedded Dolt and adding `bughunt sync` over `refs/dolt/data` is the first task of Plan 2, because sync only matters once triage verdicts exist to share.
- **Triage commands.** `store.SetStatus` exists and is tested; `bughunt triage`, `list`, and `show` are Plan 2.
- **Fixture repos.** The spec's middle testing layer — small repos with planted bugs and expected finding sets — lands in Plan 2, where the first emitted rule needs somewhere to deposit its origin case. Plan 1 covers the unit layer and the dogfood layer, which is enough to prove the adapters and fingerprints work.
- **`learn`, rule emission, lessons.** All of Plan 2.
- **`hunt`, budget, branch policy, beads escalation, agentsh-backed Runner, SKILL.md.** All of Plan 3. The `Runner` interface in Task 6 is the seam agentsh plugs into.
