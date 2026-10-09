package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type witnessFixture struct {
	workspace string
	outside   string
}

func newWitnessFixture(t *testing.T) witnessFixture {
	t.Helper()
	return witnessFixture{workspace: t.TempDir(), outside: t.TempDir()}
}

func (f witnessFixture) witnessJSON(overrides map[string]string) string {
	fields := map[string]string{
		"key_file":     filepath.Join(f.outside, "witness.key"),
		"role":         "maker",
		"agent_id":     "maker-1",
		"constitution": filepath.Join(f.outside, "line.toml"),
		"state_dir":    filepath.Join(f.outside, "state"),
		"remote":       filepath.Join(f.outside, "remote.git"),
	}
	for k, v := range overrides {
		fields[k] = v
	}
	var parts []string
	for _, k := range []string{"key_file", "role", "agent_id", "constitution", "state_dir", "remote"} {
		parts = append(parts, `"`+k+`": "`+fields[k]+`"`)
	}
	return `{"witness": {` + strings.Join(parts, ", ") + `}}`
}

func writeWitnessFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWitnessConfigOutsideWorkspaceLoads(t *testing.T) {
	f := newWitnessFixture(t)
	path := writeWitnessFile(t, filepath.Join(f.outside, "witness.json"), f.witnessJSON(nil))
	cfg, err := Load(path, f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Witness == nil || cfg.Witness.Role != "maker" || cfg.Witness.Ref != "refs/heads/master" {
		t.Fatalf("witness = %+v", cfg.Witness)
	}
}

func TestWitnessConfigInsideWorkspaceIsRejected(t *testing.T) {
	f := newWitnessFixture(t)
	path := writeWitnessFile(t, filepath.Join(f.workspace, ".agentsh", "config.json"), f.witnessJSON(nil))
	if _, err := Load("", f.workspace); err == nil {
		t.Fatalf("loaded a witness config from inside the workspace (%s)", path)
	}
}

func TestWitnessPathsInsideWorkspaceAreRejected(t *testing.T) {
	for _, field := range []string{"key_file", "constitution", "state_dir", "remote"} {
		t.Run(field, func(t *testing.T) {
			f := newWitnessFixture(t)
			doc := f.witnessJSON(map[string]string{field: filepath.Join(f.workspace, "inside")})
			path := writeWitnessFile(t, filepath.Join(f.outside, "witness.json"), doc)
			if _, err := Load(path, f.workspace); err == nil {
				t.Fatalf("accepted %s inside the workspace", field)
			}
		})
	}
}

func TestWitnessConfigRequiresFieldsAndKnownRole(t *testing.T) {
	cases := map[string]map[string]string{
		"missing role":     {"role": ""},
		"unknown role":     {"role": "overlord"},
		"missing agent_id": {"agent_id": ""},
		"missing key_file": {"key_file": ""},
	}
	for name, overrides := range cases {
		t.Run(name, func(t *testing.T) {
			f := newWitnessFixture(t)
			path := writeWitnessFile(t, filepath.Join(f.outside, "witness.json"), f.witnessJSON(overrides))
			if _, err := Load(path, f.workspace); err == nil {
				t.Fatal("accepted an invalid witness config")
			}
		})
	}
}
