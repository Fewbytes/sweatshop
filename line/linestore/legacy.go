package linestore

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Fewbytes/sweatshop/line/report"
)

// Names of the flat files the line kept its data in before the database.
const (
	legacyRuns           = "runs.jsonl"
	legacyTests          = "tests.jsonl"
	legacyKaizenSignals  = "kaizen-signals.jsonl"
	legacyHeijunkaLevels = "heijunka.json"
)

// ImportLegacy copies the line directory's flat files (runs.jsonl, tests.jsonl,
// kaizen-signals.jsonl, heijunka.json) into the database. Each file is
// imported once: the database remembers it, so a restart never duplicates
// rows and later edits to a file are ignored. Missing files are skipped and
// the files are left in place. Torn or malformed JSONL lines are skipped, as
// the old readers did.
func (s *Store) ImportLegacy(ctx context.Context, dir string) error {
	imports := []struct {
		file string
		load func(ctx context.Context, tx *sql.Tx, path string) error
	}{
		{legacyRuns, importRuns},
		{legacyTests, importTests},
		{legacyKaizenSignals, importKaizenSignals},
		{legacyHeijunkaLevels, importLevels},
	}
	for _, imp := range imports {
		path := filepath.Join(dir, imp.file)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := s.importOnce(ctx, imp.file, func(tx *sql.Tx) error { return imp.load(ctx, tx, path) }); err != nil {
			return fmt.Errorf("import %s: %w", imp.file, err)
		}
	}
	return nil
}

func (s *Store) importOnce(ctx context.Context, file string, load func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var done int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM imported WHERE file = ?`, file).Scan(&done); err != nil {
		return err
	}
	if done > 0 {
		return nil
	}
	if err := load(tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO imported (file) VALUES (?)`, file); err != nil {
		return err
	}
	return tx.Commit()
}

func importRuns(ctx context.Context, tx *sql.Tx, path string) error {
	runs, err := report.ReadJSONL(path)
	if err != nil {
		return err
	}
	for _, st := range runs {
		if err := insertRun(ctx, tx, st); err != nil {
			return err
		}
	}
	return nil
}

func importTests(ctx context.Context, tx *sql.Tx, path string) error {
	rows, err := report.ReadTestsJSONL(path)
	if err != nil {
		return err
	}
	return insertTests(ctx, tx, rows)
}

func importKaizenSignals(ctx context.Context, tx *sql.Tx, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var rec struct {
			Commit  string          `json:"commit"`
			Signals json.RawMessage `json:"signals"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO kaizen_baseline (commit_sha, signals) VALUES (?, ?)`, rec.Commit, string(rec.Signals)); err != nil {
			return err
		}
	}
	return sc.Err()
}

func importLevels(ctx context.Context, tx *sql.Tx, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var l Levels
	if err := json.Unmarshal(data, &l); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO levels (id, agents, wip_limit) VALUES (1, ?, ?)`, l.Agents, l.WIPLimit)
	return err
}
