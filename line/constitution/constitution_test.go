package constitution

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func containerDoc(encoded, extraRunner string) string {
	doc := strings.Replace(valid(encoded), `kind = "local"`, "kind = \"container\"\nimage = \"ci:1\""+extraRunner, 1)
	return strings.Replace(doc, `level = "none"`, `level = "attest"`, 1)
}

func TestRunnerLimitsAndCheckTimeoutDefault(t *testing.T) {
	_, encoded := publicKey(t)
	c, err := Parse([]byte(containerDoc(encoded, "")))
	if err != nil {
		t.Fatal(err)
	}
	if c.Runner.Memory != "4g" || c.Runner.Pids != 1024 || c.Runner.CPUs != "2" {
		t.Fatalf("runner limits = %+v", c.Runner)
	}
	if got := c.Checks.TimeoutDuration(); got != 30*time.Minute {
		t.Fatalf("check timeout = %s", got)
	}
	// A zero-value constitution (built in code) gets the same defaults.
	if got := (Checks{}).TimeoutDuration(); got != 30*time.Minute {
		t.Fatalf("zero check timeout = %s", got)
	}
	if l := (Runner{}).Limits(); l.Memory != "4g" || l.Pids != 1024 || l.CPUs != "2" {
		t.Fatalf("zero runner limits = %+v", l)
	}
}

func TestRunnerLimitsConfigurableAndOutsideID(t *testing.T) {
	_, encoded := publicKey(t)
	doc := containerDoc(encoded, "\nmemory = \"512m\"\npids = 64\ncpus = \"0.5\"")
	doc = strings.Replace(doc, "[[checks.required]]", "[checks]\ntimeout = \"90s\"\n\n[[checks.required]]", 1)
	c, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if c.Runner.Memory != "512m" || c.Runner.Pids != 64 || c.Runner.CPUs != "0.5" {
		t.Fatalf("runner limits = %+v", c.Runner)
	}
	if got := c.Checks.TimeoutDuration(); got != 90*time.Second {
		t.Fatalf("check timeout = %s", got)
	}
	// Limits are not part of the runner identity: existing attestations stay
	// comparable when an operator tunes them.
	if got := c.Runner.ID(); got != "container:docker:ci:1" {
		t.Fatalf("Runner.ID() = %q", got)
	}
}

func TestRunnerLimitsRejected(t *testing.T) {
	_, encoded := publicKey(t)
	cases := map[string]string{
		"bad memory":       containerDoc(encoded, "\nmemory = \"lots\""),
		"option in memory": containerDoc(encoded, "\nmemory = \"--privileged\""),
		"unlimited memory": containerDoc(encoded, "\nmemory = \"0\""),
		"negative pids":    containerDoc(encoded, "\npids = -1"),
		"bad cpus":         containerDoc(encoded, "\ncpus = \"two\""),
		"zero cpus":        containerDoc(encoded, "\ncpus = \"0\""),
		"bad timeout":      strings.Replace(containerDoc(encoded, ""), "[[checks.required]]", "[checks]\ntimeout = \"soon\"\n\n[[checks.required]]", 1),
		"zero timeout":     strings.Replace(containerDoc(encoded, ""), "[[checks.required]]", "[checks]\ntimeout = \"0s\"\n\n[[checks.required]]", 1),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(doc)); err == nil {
				t.Fatalf("accepted:\n%s", doc)
			}
		})
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

func TestGovernance(t *testing.T) {
	_, key := publicKey(t)
	cases := []struct {
		name, extra, wantErr string
	}{
		{"absent", "", ""},
		{"full", `
[sovereign]
name = "k"
discord = "123"
[channel]
kind = "discord"
channel = "456"
[viceroy]
succession = true
[[viceroy.delegations]]
offer = "line"
types = ["bug"]
[[viceroy.delegations]]
offer = "line"
parent = "sweatshop-0mw.2"
`, ""},
		{"bad discord", "[sovereign]\nname=\"k\"\ndiscord=\"abc\"\n", "sovereign.discord"},
		{"bad kind", "[channel]\nkind=\"slack\"\nchannel=\"1\"\n", "channel.kind"},
		{"bad transport", "[channel]\nkind=\"discord\"\nchannel=\"1\"\ntransport=\"smoke\"\n", "channel.transport"},
		{"bad channel", "[channel]\nkind=\"discord\"\nchannel=\"x\"\n", "channel.channel"},
		{"bad offer", "[[viceroy.delegations]]\noffer=\"all\"\ntypes=[\"bug\"]\n", "offer"},
		{"empty delegation", "[[viceroy.delegations]]\noffer=\"line\"\n", "types or parent"},
		{"bad parent", "[[viceroy.delegations]]\noffer=\"line\"\nparent=\"A B\"\n", "parent"},
		{"unknown field", "[viceroy]\nking = true\n", "strict mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(valid(key) + tc.extra))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want mention of %q", err, tc.wantErr)
			}
		})
	}
}

func TestViceroyRole(t *testing.T) {
	_, key := publicKey(t)
	extra := "[[guards.paths]]\npattern = \"x/**\"\nroles = [\"viceroy\"]\n"
	if _, err := Parse([]byte(valid(key) + extra)); err != nil {
		t.Fatal(err)
	}
}

func TestDelegates(t *testing.T) {
	c := Constitution{Viceroy: Viceroy{Delegations: []Delegation{
		{Offer: "line", Types: []string{"bug"}},
		{Offer: "line", Parent: "m-2"},
	}}}
	cases := []struct {
		name      string
		typ       string
		ancestors []string
		want      bool
	}{
		{"type match", "bug", nil, true},
		{"parent match", "task", []string{"m-2.1", "m-2"}, true},
		{"no match", "task", []string{"m-3"}, false},
		{"empty", "", nil, false},
	}
	for _, tc := range cases {
		if got := c.Delegates(tc.typ, tc.ancestors); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	if (Constitution{}).Delegates("bug", []string{"m-2"}) {
		t.Error("empty constitution delegates")
	}
}

func TestExampleParses(t *testing.T) {
	c, _, err := Load("testdata/example.toml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Trust.Level != "attest" || len(c.Trust.Keys) != 1 || c.Runner.Kind != "container" ||
		len(c.Checks.Required) != 1 || len(c.Guards.Paths) != 2 ||
		c.Sovereign.Discord == "" || c.Channel.Kind != "discord" ||
		!c.Viceroy.Succession || len(c.Viceroy.Delegations) != 2 {
		t.Fatalf("example parsed unexpectedly: %+v", c)
	}
}

func TestCardsCommentCapDefaultsAndRejectsNegative(t *testing.T) {
	_, key := publicKey(t)
	base := valid(key)
	c, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Cards.CommentCapOrDefault(); got != DefaultCommentCap {
		t.Fatalf("default cap %d", got)
	}
	c, err = Parse([]byte("[cards]\ncomment_cap = 3\n" + base))
	if err != nil || c.Cards.CommentCapOrDefault() != 3 {
		t.Fatalf("explicit cap: %+v %v", c.Cards, err)
	}
	if _, err := Parse([]byte("[cards]\ncomment_cap = -1\n" + base)); err == nil {
		t.Fatal("negative cap accepted")
	}
}
