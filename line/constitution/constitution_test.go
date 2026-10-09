package constitution

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/attest"
)

func publicKey(t *testing.T) (ed25519.PublicKey, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, attest.EncodePublicKey(pub)
}

func valid(key string) string {
	return fmt.Sprintf(`[trust]
level = "none"

[[trust.keys]]
name = "witness-a"
public_key = %q

[runner]
kind = "local"

[[checks.required]]
name = "ci"
command = "just ci"

[[guards.paths]]
pattern = "acceptance/**"
roles = ["gemba"]
`, key)
}

func TestParseValid(t *testing.T) {
	pub, encoded := publicKey(t)
	c, err := Parse([]byte(valid(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Checks.Required[0].Command != "just ci" || c.Runner.ID() != "local" {
		t.Fatalf("unexpected constitution: %+v", c)
	}
	keys, err := c.TrustedKeys()
	if err != nil {
		t.Fatal(err)
	}
	if !keys[attest.KeyID(pub)].Equal(pub) {
		t.Fatal("trusted key not indexed by key ID")
	}
}

func TestKeyRoles(t *testing.T) {
	pub, encoded := publicKey(t)
	doc := strings.Replace(valid(encoded), "public_key = "+fmt.Sprintf("%q", encoded), "public_key = "+fmt.Sprintf("%q", encoded)+"\nroles = [\"maker\"]", 1)
	c, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	roles, err := c.KeyRoles()
	if err != nil {
		t.Fatal(err)
	}
	if got := roles[attest.KeyID(pub)]; len(got) != 1 || got[0] != "maker" {
		t.Fatalf("KeyRoles = %v", roles)
	}
}

func TestLoadDigestTracksContent(t *testing.T) {
	_, encoded := publicKey(t)
	dir := t.TempDir()
	a := filepath.Join(dir, "a.toml")
	b := filepath.Join(dir, "b.toml")
	if err := os.WriteFile(a, []byte(valid(encoded)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte(valid(encoded)+"\n# amended\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, digestA, err := Load(a)
	if err != nil {
		t.Fatal(err)
	}
	_, digestA2, err := Load(a)
	if err != nil {
		t.Fatal(err)
	}
	_, digestB, err := Load(b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(digestA, "sha256:") || digestA != digestA2 || digestA == digestB {
		t.Fatalf("digests a=%s a2=%s b=%s", digestA, digestA2, digestB)
	}
}

func TestParseRejects(t *testing.T) {
	_, encoded := publicKey(t)
	base := valid(encoded)
	cases := map[string]string{
		"unknown field":            base + "\n[extra]\nx = 1\n",
		"attest with local runner": strings.Replace(base, `level = "none"`, `level = "attest"`, 1),
		"container without image":  strings.Replace(base, `kind = "local"`, `kind = "container"`, 1),
		"bad trust level":          strings.Replace(base, `level = "none"`, `level = "full"`, 1),
		"bad runner kind":          strings.Replace(base, `kind = "local"`, `kind = "vm"`, 1),
		"no required checks":       strings.Replace(base, "[[checks.required]]\nname = \"ci\"\ncommand = \"just ci\"\n", "", 1),
		"duplicate check":          base + "\n[[checks.required]]\nname = \"ci\"\ncommand = \"go test ./...\"\n",
		"check without command":    base + "\n[[checks.required]]\nname = \"lint\"\n",
		"bad public key":           strings.Replace(base, encoded, "AAAA", 1),
		"unknown role":             strings.Replace(base, `roles = ["gemba"]`, `roles = ["boss"]`, 1),
		"unknown key role":         strings.Replace(base, "public_key = "+fmt.Sprintf("%q", encoded), "public_key = "+fmt.Sprintf("%q", encoded)+"\nroles = [\"boss\"]", 1),
		"malformed guard pattern":  strings.Replace(base, `pattern = "acceptance/**"`, `pattern = "acceptance/[x"`, 1),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(doc)); err == nil {
				t.Fatalf("accepted:\n%s", doc)
			}
		})
	}
}

func TestContainerRunnerDefaultsEngineAndID(t *testing.T) {
	_, encoded := publicKey(t)
	doc := strings.Replace(valid(encoded), `kind = "local"`, "kind = \"container\"\nimage = \"ghcr.io/x/ci@sha256:abc\"", 1)
	doc = strings.Replace(doc, `level = "none"`, `level = "attest"`, 1)
	c, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Runner.ID(); got != "container:docker:ghcr.io/x/ci@sha256:abc" {
		t.Fatalf("Runner.ID() = %q", got)
	}
}

func TestMatchPath(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"acceptance/**", "acceptance/login_test.go", true},
		{"acceptance/**", "acceptance/deep/nested.go", true},
		{"acceptance/**", "acceptance", true},
		{"acceptance/**", "acceptancex/file", false},
		{"*.md", "README.md", true},
		{"*.md", "docs/README.md", false},
		{"line.toml", "line.toml", true},
	}
	for _, c := range cases {
		if got := MatchPath(c.pattern, c.path); got != c.want {
			t.Errorf("MatchPath(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestAllowedRoles(t *testing.T) {
	_, encoded := publicKey(t)
	c, err := Parse([]byte(valid(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	roles, guarded := c.AllowedRoles("acceptance/spec_test.go")
	if !guarded || len(roles) != 1 || roles[0] != "gemba" {
		t.Fatalf("AllowedRoles = %v, %v", roles, guarded)
	}
	if _, guarded := c.AllowedRoles("src/main.go"); guarded {
		t.Fatal("unguarded path reported as guarded")
	}
}
