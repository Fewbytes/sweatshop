package station

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenStoreServesRunsImportedFromLegacyFiles(t *testing.T) {
	r := newRig(t)
	dir := t.TempDir()
	legacy := `{"RunID":"old/1","Commit":"old","Passed":true,"Checks":1}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "runs.jsonl"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.srv.Close(); err != nil { // the rig's own database
		t.Fatal(err)
	}
	if err := r.srv.OpenStore(r.ctx, dir); err != nil {
		t.Fatal(err)
	}
	runs, err := r.maker.PassingRuns(r.ctx, "old")
	if err != nil || len(runs) != 1 || runs[0].Commit != "old" {
		t.Fatalf("runs = %+v, %v; want the imported legacy run", runs, err)
	}
}
