package quadlet

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/secret"
)

func testSpec(name string, port int) Spec {
	return Spec{
		Name: name, Dir: "/srv/lines/" + name, Port: port, ProjectPath: "/srv/projects/" + name,
		Image: "localhost/sweatshop-line:dev", CPUQuota: "200%", MemoryMax: "8G",
	}
}

func renderByName(t *testing.T, s Spec) map[string]string {
	t.Helper()
	files, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range files {
		out[f.Name] = f.Content
	}
	return out
}

func requireContains(t *testing.T, file, content string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(content, w) {
			t.Errorf("%s lacks %q:\n%s", file, w, content)
		}
	}
}

func TestRenderGivesEachLineItsOwnNetworkSliceAndPort(t *testing.T) {
	a := renderByName(t, testSpec("alpha", 7468))
	b := renderByName(t, testSpec("beta", 7469))

	requireContains(t, "alpha network", a["sweatshop-alpha.network"], "NetworkName=sweatshop-alpha")
	requireContains(t, "alpha slice", a["sweatshop-alpha.slice"], "CPUQuota=200%", "MemoryMax=8G")
	requireContains(t, "alpha state volume", a["sweatshop-alpha-state.volume"], "Device=/srv/lines/alpha", "Options=bind")
	requireContains(t, "alpha serve", a["sweatshop-alpha-serve.container"],
		"Network=sweatshop-alpha.network",
		"Slice=sweatshop-alpha.slice",
		"PublishPort=127.0.0.1:7468:7468",
		"Volume=%t/podman/podman.sock:/run/podman/podman.sock",
		"--dir /srv/lines/alpha", "--beads /srv/projects/alpha")
	requireContains(t, "beta serve", b["sweatshop-beta-serve.container"],
		"PublishPort=127.0.0.1:7469:7469", "Network=sweatshop-beta.network", "--beads /srv/projects/beta")

	for name, content := range a {
		if strings.Contains(content, "beta") {
			t.Errorf("%s of line alpha mentions beta:\n%s", name, content)
		}
	}
}

func TestRenderStationsReachTheirOwnLinesServeByName(t *testing.T) {
	files := renderByName(t, testSpec("alpha", 7468))
	for _, role := range []string{"heijunka", "andon", "kaizen"} {
		requireContains(t, role, files["sweatshop-alpha-"+role+".container"],
			"Environment=SWEATSHOP_LINE_URL=http://sweatshop-alpha-serve:7468",
			"EnvironmentFile=/srv/lines/alpha/stations/"+role+"-1/env")
	}
	if strings.Contains(files["sweatshop-alpha-andon.container"], "podman.sock") {
		t.Error("andon does not start containers but mounts the podman socket")
	}
	requireContains(t, "kaizen", files["sweatshop-alpha-kaizen.container"], "podman.sock")
}

func TestRenderRejectsUnsafeSpecs(t *testing.T) {
	for name, mutate := range map[string]func(*Spec){
		"uppercase name":      func(s *Spec) { s.Name = "Alpha" },
		"path in name":        func(s *Spec) { s.Name = "../x" },
		"relative dir":        func(s *Spec) { s.Dir = "lines/alpha" },
		"relative project":    func(s *Spec) { s.ProjectPath = "proj" },
		"zero port":           func(s *Spec) { s.Port = 0 },
		"newline injects key": func(s *Spec) { s.MemoryMax = "8G\nExecStartPre=/bin/sh" },
		"specifier in path":   func(s *Spec) { s.ProjectPath = "/srv/%h" },
		"missing image":       func(s *Spec) { s.Image = "" },
	} {
		s := testSpec("alpha", 7468)
		mutate(&s)
		if _, err := Render(s); err == nil {
			t.Errorf("%s: rendered %+v", name, s)
		}
	}
}

func TestInstallWritesSliceApartFromQuadletFiles(t *testing.T) {
	qdir, sdir := filepath.Join(t.TempDir(), "q"), filepath.Join(t.TempDir(), "s")
	written, err := Install(testSpec("alpha", 7468), qdir, sdir)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 7 {
		t.Fatalf("wrote %d files, want 7: %v", len(written), written)
	}
	if _, err := os.Stat(filepath.Join(sdir, "sweatshop-alpha.slice")); err != nil {
		t.Errorf("slice not in slice dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(qdir, "sweatshop-alpha-serve.container")); err != nil {
		t.Errorf("serve unit not in quadlet dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(qdir, "sweatshop-alpha.slice")); err == nil {
		t.Error("slice written into the quadlet dir")
	}
}

func TestRenderMirrorServiceMountsOnlyItsOwnLinesCredentials(t *testing.T) {
	if _, ok := renderByName(t, testSpec("alpha", 7468))["sweatshop-alpha-mirror.container"]; ok {
		t.Error("a line without mirror credentials got a mirror service")
	}
	s := testSpec("alpha", 7468)
	s.MirrorCredDir = "/srv/creds/alpha"
	files := renderByName(t, s)
	requireContains(t, "alpha mirror", files["sweatshop-alpha-mirror.container"],
		"Exec=sweatshop line mirror --dir /srv/lines/alpha --creds /srv/creds/alpha",
		"Volume=/srv/creds/alpha:/srv/creds/alpha:ro",
		"Network=sweatshop-alpha.network")
	for name, content := range files {
		if name != "sweatshop-alpha-mirror.container" && strings.Contains(content, "/srv/creds") {
			t.Errorf("%s mounts the mirror credentials:\n%s", name, content)
		}
	}
	s.MirrorCredDir = "creds"
	if _, err := Render(s); err == nil {
		t.Error("rendered a relative mirror credential dir")
	}
}

// envLine makes a line directory with the stations whose units read env files.
func envLine(t *testing.T) Spec {
	t.Helper()
	dir := t.TempDir()
	for _, st := range []string{"heijunka-1", "andon-1", "kaizen-1"} {
		if err := os.MkdirAll(filepath.Join(dir, "stations", st), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "stations", st, "token"), []byte("tok-"+st+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Spec{Name: "alpha", Dir: dir}
}

type fakeModelCred struct{ value string }

func (f fakeModelCred) Read(name string) (string, error) {
	if name != secret.ModelCred || f.value == "" {
		return "", secret.ErrNotFound
	}
	return f.value, nil
}

func readEnv(t *testing.T, s Spec, station string) string {
	t.Helper()
	path := filepath.Join(s.Dir, "stations", station, "env")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("%s mode = %o, want 600", path, info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWriteEnvFilesGivesEachUnitItsTokenAndKaizenTheModelCredential(t *testing.T) {
	s := envLine(t)
	written, err := WriteEnvFiles(s, fakeModelCred{"model-secret"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 3 {
		t.Errorf("written = %v, want 3 files", written)
	}
	if got := readEnv(t, s, "andon-1"); got != "SWEATSHOP_LINE_TOKEN=tok-andon-1\n" {
		t.Errorf("andon env = %q", got)
	}
	if got := readEnv(t, s, "heijunka-1"); got != "SWEATSHOP_LINE_TOKEN=tok-heijunka-1\n" {
		t.Errorf("heijunka env = %q", got)
	}
	if got, want := readEnv(t, s, "kaizen-1"), "SWEATSHOP_LINE_TOKEN=tok-kaizen-1\nCLAUDE_CODE_OAUTH_TOKEN=model-secret\n"; got != want {
		t.Errorf("kaizen env = %q, want %q", got, want)
	}
}

func TestWriteEnvFilesKeepsExistingFileUnlessRewriteIsSet(t *testing.T) {
	s := envLine(t)
	andon := filepath.Join(s.Dir, "stations", "andon-1", "env")
	if err := os.WriteFile(andon, []byte("EDITED=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteEnvFiles(s, fakeModelCred{"m"}, false); err != nil {
		t.Fatal(err)
	}
	if got := readEnv(t, s, "andon-1"); got != "EDITED=1\n" {
		t.Errorf("existing env overwritten: %q", got)
	}
	if _, err := WriteEnvFiles(s, fakeModelCred{"m"}, true); err != nil {
		t.Fatal(err)
	}
	if got := readEnv(t, s, "andon-1"); got != "SWEATSHOP_LINE_TOKEN=tok-andon-1\n" {
		t.Errorf("rewrite kept old env: %q", got)
	}
}

func TestWriteEnvFilesFailsWithoutModelCredentialOrStation(t *testing.T) {
	s := envLine(t)
	if _, err := WriteEnvFiles(s, fakeModelCred{}, false); !errors.Is(err, secret.ErrNotFound) {
		t.Errorf("missing model credential: err = %v, want ErrNotFound", err)
	}
	if err := os.RemoveAll(filepath.Join(s.Dir, "stations", "heijunka-1")); err != nil {
		t.Fatal(err)
	}
	_, err := WriteEnvFiles(s, fakeModelCred{"m"}, true)
	if err == nil || !strings.Contains(err.Error(), "heijunka-1") {
		t.Errorf("missing station: err = %v, want one naming heijunka-1", err)
	}
}
