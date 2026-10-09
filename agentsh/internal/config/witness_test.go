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

// A workspace config without a witness section must not hide the operator's
// witness binding: the agent can create that file.
func TestWorkspaceConfigDoesNotShadowWitness(t *testing.T) {
	f := newWitnessFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	operator := writeWitnessFile(t, filepath.Join(home, ".config", "agentsh", "config.json"), f.witnessJSON(nil))
	workspace := writeWitnessFile(t, filepath.Join(f.workspace, ".agentsh", "config.json"), `{"turso": {}}`)
	cfg, err := Load("", f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Witness == nil || cfg.Witness.Role != "maker" {
		t.Fatalf("witness = %+v; the workspace config shadowed it", cfg.Witness)
	}
	if cfg.Path != workspace || cfg.WitnessPath != operator {
		t.Fatalf("Path = %s, WitnessPath = %s", cfg.Path, cfg.WitnessPath)
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

func TestWitnessContainmentResolvesSymlinks(t *testing.T) {
	cases := map[string]struct {
		workspace, field, path string
	}{
		"alias workspace, real key":      {"alias/ws", "key_file", "real/ws/k"},
		"real workspace, alias key":      {"real/ws", "key_file", "alias/ws/k"},
		"alias workspace, new state_dir": {"alias/ws", "state_dir", "real/ws/new/state"},
		"real workspace, new alias dir":  {"real/ws", "state_dir", "alias/ws/new/state"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			if err := os.MkdirAll(filepath.Join(base, "real", "ws"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(base, "real"), filepath.Join(base, "alias")); err != nil {
				t.Fatal(err)
			}
			f := witnessFixture{workspace: filepath.Join(base, tc.workspace), outside: t.TempDir()}
			doc := f.witnessJSON(map[string]string{tc.field: filepath.Join(base, tc.path)})
			path := writeWitnessFile(t, filepath.Join(f.outside, "witness.json"), doc)
			if _, err := Load(path, f.workspace); err == nil {
				t.Fatalf("accepted %s %s inside workspace %s via symlink", tc.field, tc.path, tc.workspace)
			}
		})
	}
}

func TestWitnessRefIsValidated(t *testing.T) {
	for ref, ok := range map[string]bool{
		"refs/heads/main": true, "heads/main": false, "refs/heads/../x": false, "/refs/heads/x": false,
	} {
		f := newWitnessFixture(t)
		doc := strings.Replace(f.witnessJSON(nil), `"remote"`, `"ref": "`+ref+`", "remote"`, 1)
		path := writeWitnessFile(t, filepath.Join(f.outside, "witness.json"), doc)
		_, err := Load(path, f.workspace)
		if ok != (err == nil) {
			t.Errorf("ref %q: err = %v, want ok=%v", ref, err, ok)
		}
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
