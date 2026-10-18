package constitution

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
		{"empty delegation", "[[viceroy.delegations]]\noffer=\"line\"\n", "types, parent or inherit"},
		{"bad parent", "[[viceroy.delegations]]\noffer=\"line\"\nparent=\"A B\"\n", "parent"},
		{"close by ids", "[[viceroy.delegations]]\nclose=true\nids=[\"m-1\"]\n", ""},
		{"close by parent", "[[viceroy.delegations]]\nclose=true\nparent=\"m-2\"\n", ""},
		{"unbounded close", "[[viceroy.delegations]]\nclose=true\n", "close needs ids, parent, types or labels"},
		{"close by type", "[[viceroy.delegations]]\nclose=true\ntypes=[\"bug\"]\n", ""},
		{"close with offer", "[[viceroy.delegations]]\nclose=true\noffer=\"line\"\nids=[\"m-1\"]\n", "close takes only"},
		{"bad close id", "[[viceroy.delegations]]\nclose=true\nids=[\"A B\"]\n", "ids"},
		{"ids on offer", "[[viceroy.delegations]]\noffer=\"line\"\nids=[\"m-1\"]\n", "only a close delegation"},
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
		if got := c.Delegates(tc.typ, tc.ancestors, false); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	if (Constitution{}).Delegates("bug", []string{"m-2"}, false) {
		t.Error("empty constitution delegates")
	}
}

func TestDelegatesInheritCoversNonEpicsUnderApprovedEpic(t *testing.T) {
	c := Constitution{Viceroy: Viceroy{Delegations: []Delegation{{Offer: "line", Inherit: true}}}}
	cases := []struct {
		name     string
		typ      string
		approved bool
		want     bool
	}{
		{"task under approved epic", "task", true, true},
		{"feature under approved epic", "feature", true, true},
		{"task without approved epic", "task", false, false},
		{"epic is never covered", EpicType, true, false},
	}
	for _, tc := range cases {
		if got := c.Delegates(tc.typ, []string{"m-2"}, tc.approved); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	if err := (Constitution{Viceroy: Viceroy{Delegations: []Delegation{{Close: true, Inherit: true, Types: []string{"bug"}}}}}).validateGovernance(); err == nil {
		t.Error("close delegation accepted inherit")
	}
	if err := c.validateGovernance(); err != nil {
		t.Errorf("inherit-only offer rejected: %v", err)
	}
}

func TestDelegatesClose(t *testing.T) {
	c := Constitution{Viceroy: Viceroy{Delegations: []Delegation{
		{Close: true, IDs: []string{"m-1"}},
		{Close: true, Parent: "m-2"},
		{Offer: "line", Types: []string{"bug"}, Parent: "m-3"},
		{Close: true, Types: []string{"chore"}, Labels: []string{"obsolete"}},
	}}}
	cases := []struct {
		name      string
		id        string
		typ       string
		labels    []string
		ancestors []string
		want      bool
	}{
		{"listed id", "m-1", "task", nil, nil, true},
		{"under close parent", "m-2.1", "task", nil, []string{"m-2"}, true},
		{"unlisted", "m-4", "task", nil, nil, false},
		{"offer delegation grants no close", "m-3.1", "bug", nil, []string{"m-3"}, false},
		{"close type", "m-5", "chore", nil, nil, true},
		{"close label", "m-6", "task", []string{"x", "obsolete"}, nil, true},
		{"other label", "m-6", "task", []string{"x"}, nil, false},
		{"empty type matches nothing", "m-7", "", nil, nil, false},
	}
	for _, tc := range cases {
		if got := c.DelegatesClose(tc.id, tc.typ, tc.labels, tc.ancestors); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	if c.Delegates("task", []string{"m-2"}, false) {
		t.Error("close delegation grants offer")
	}
}

func TestCloseDelegationCategoriesValidate(t *testing.T) {
	_, key := publicKey(t)
	cases := []struct {
		name, extra string
		ok          bool
	}{
		{"close by labels", "close = true\nlabels = [\"obsolete\"]\n", true},
		{"close by types", "close = true\ntypes = [\"chore\"]\n", true},
		{"close empty label", "close = true\nlabels = [\"\"]\n", false},
		{"close with no bound", "close = true\n", false},
		{"offer with labels", "offer = \"line\"\ntypes = [\"bug\"]\nlabels = [\"x\"]\n", false},
	}
	for _, tc := range cases {
		_, err := Parse([]byte(valid(key) + "[[viceroy.delegations]]\n" + tc.extra))
		if (err == nil) != tc.ok {
			t.Errorf("%s: err %v", tc.name, err)
		}
	}
}

func TestViceroyMayLabel(t *testing.T) {
	v := Viceroy{Labels: []string{"obsolete", "triage:*"}}
	for label, want := range map[string]bool{"obsolete": true, "triage:a": true, "triage:": true, "obsolete2": false, "line": false} {
		if got := v.MayLabel(label); got != want {
			t.Errorf("MayLabel(%q) = %v, want %v", label, got, want)
		}
	}
	if (Viceroy{}).MayLabel("obsolete") {
		t.Error("empty list grants a label")
	}
	_, key := publicKey(t)
	if _, err := Parse([]byte(valid(key) + "[viceroy]\nlabels = [\"\"]\n")); err == nil {
		t.Error("empty viceroy label accepted")
	}
}

func TestExampleParses(t *testing.T) {
	c, _, err := Load("testdata/example.toml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Trust.Level != "attest" || len(c.Trust.Keys) != 1 || c.Runner.Kind != "container" ||
		len(c.Checks.Required) != 1 || len(c.Guards.Paths) != 2 || len(c.Remotes) != 2 || c.Guards.Blobs.MaxBytes != 1<<20 || !c.Guards.Blobs.Allows("testdata/x.bin") ||
		c.Sovereign.Discord == "" || c.Channel.Kind != "discord" ||
		!c.Viceroy.Succession || len(c.Viceroy.Delegations) != 4 {
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

func TestBlobGuardDefaultsAllowListAndRejectsBad(t *testing.T) {
	_, key := publicKey(t)
	base := valid(key)
	c, err := Parse([]byte(base))
	if err != nil || c.Guards.Blobs.MaxBytesOrDefault() != DefaultBlobMaxBytes || c.Guards.Blobs.Allows("bin/tool") {
		t.Fatalf("default: %+v %v", c.Guards.Blobs, err)
	}
	c, err = Parse([]byte("[guards.blobs]\nmax_bytes = 10\nallow = [\"vendor/**\", \"*.wasm\"]\n" + base))
	if err != nil || c.Guards.Blobs.MaxBytesOrDefault() != 10 {
		t.Fatalf("explicit: %+v %v", c.Guards.Blobs, err)
	}
	if !c.Guards.Blobs.Allows("vendor/lib/a.so") || !c.Guards.Blobs.Allows("m.wasm") || c.Guards.Blobs.Allows("bin/tool") {
		t.Fatalf("allow list matched wrongly: %+v", c.Guards.Blobs)
	}
	for _, bad := range []string{"max_bytes = -1", "allow = [\"[\"]"} {
		if _, err := Parse([]byte("[guards.blobs]\n" + bad + "\n" + base)); err == nil {
			t.Errorf("guards.blobs %q accepted", bad)
		}
	}
}

func TestCardsNeedsTimeoutDefaultsAndRejectsBad(t *testing.T) {
	_, key := publicKey(t)
	base := valid(key)
	c, err := Parse([]byte(base))
	if err != nil || c.Cards.NeedsTimeoutOrDefault() != DefaultNeedsTimeout {
		t.Fatalf("default: %v %v", c.Cards.NeedsTimeoutOrDefault(), err)
	}
	c, err = Parse([]byte("[cards]\nneeds_timeout = \"6h\"\n" + base))
	if err != nil || c.Cards.NeedsTimeoutOrDefault() != 6*time.Hour {
		t.Fatalf("explicit: %+v %v", c.Cards, err)
	}
	for _, bad := range []string{"soon", "-1h", "0s"} {
		if _, err := Parse([]byte("[cards]\nneeds_timeout = \"" + bad + "\"\n" + base)); err == nil {
			t.Errorf("needs_timeout %q accepted", bad)
		}
	}
}

func TestViceroyPresignMaxTTLDefaultsAndRejectsBad(t *testing.T) {
	_, key := publicKey(t)
	base := valid(key)
	c, err := Parse([]byte(base))
	if err != nil || c.Viceroy.PresignMaxTTLOrDefault() != DefaultPresignMaxTTL {
		t.Fatalf("default: %v %v", c.Viceroy.PresignMaxTTLOrDefault(), err)
	}
	c, err = Parse([]byte("[viceroy]\npresign_max_ttl = \"15m\"\n" + base))
	if err != nil || c.Viceroy.PresignMaxTTLOrDefault() != 15*time.Minute {
		t.Fatalf("explicit: %+v %v", c.Viceroy, err)
	}
	for _, bad := range []string{"soon", "-1h", "0s"} {
		if _, err := Parse([]byte("[viceroy]\npresign_max_ttl = \"" + bad + "\"\n" + base)); err == nil {
			t.Errorf("presign_max_ttl %q accepted", bad)
		}
	}
}

func TestChecksColdTimeoutDefaultsAndRejectsBad(t *testing.T) {
	if got := (Checks{}).ColdTimeoutDuration(); got != defaultColdCheckTimeout {
		t.Errorf("default cold timeout = %s, want %s", got, defaultColdCheckTimeout)
	}
	if got := (Checks{ColdTimeout: "3h"}).ColdTimeoutDuration(); got != 3*time.Hour {
		t.Errorf("cold timeout = %s, want 3h", got)
	}
	c := Constitution{Runner: Runner{Kind: "local"}, Checks: Checks{ColdTimeout: "soon"}}
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "checks.cold_timeout") {
		t.Errorf("validate() = %v, want a checks.cold_timeout error", err)
	}
}

func TestRemotesDeclareAuthoritativeAndStorageMirrors(t *testing.T) {
	_, encoded := publicKey(t)
	c, err := Parse([]byte(valid(encoded) + `
[[remotes]]
name = "line"
kind = "authoritative"

[[remotes]]
name = "github"
kind = "storage"
url = "git@github.com:o/r.git"

[[remotes]]
name = "backup"
kind = "storage"
url = "https://example.com/r.git"
publish = ["refs/heads/master"]
`))
	if err != nil {
		t.Fatal(err)
	}
	if auth, ok := c.AuthoritativeRemote(); !ok || auth.Name != "line" {
		t.Fatalf("authoritative = %+v, %v; want line", auth, ok)
	}
	mirrors := c.StorageMirrors()
	if len(mirrors) != 2 || mirrors[0].Name != "github" || mirrors[1].Name != "backup" {
		t.Fatalf("mirrors = %+v; want github, backup", mirrors)
	}
	if got := mirrors[0].PublishRefs(); !slices.Equal(got, DefaultPublishRefs) {
		t.Fatalf("default publish refs = %v; want %v", got, DefaultPublishRefs)
	}
	if got := mirrors[1].PublishRefs(); !slices.Equal(got, []string{"refs/heads/master"}) {
		t.Fatalf("explicit publish refs = %v", got)
	}
}

func TestNoRemotesMeansNoAuthoritativeDeclaration(t *testing.T) {
	_, encoded := publicKey(t)
	c, err := Parse([]byte(valid(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.AuthoritativeRemote(); ok || len(c.StorageMirrors()) != 0 {
		t.Fatalf("remotes = %+v; want none", c.Remotes)
	}
}

func TestParseRejectsBadRemotes(t *testing.T) {
	_, encoded := publicKey(t)
	base := valid(encoded)
	const auth = "\n[[remotes]]\nname = \"line\"\nkind = \"authoritative\"\n"
	cases := map[string]string{
		"mirrors without an authoritative remote": base + "\n[[remotes]]\nname = \"gh\"\nkind = \"storage\"\nurl = \"u\"\n",
		"two authoritative remotes":               base + auth + "\n[[remotes]]\nname = \"two\"\nkind = \"authoritative\"\n",
		"mirror kind other than storage":          base + auth + "\n[[remotes]]\nname = \"gh\"\nkind = \"source\"\nurl = \"u\"\n",
		"storage without url":                     base + auth + "\n[[remotes]]\nname = \"gh\"\nkind = \"storage\"\n",
		"authoritative with url":                  base + "\n[[remotes]]\nname = \"line\"\nkind = \"authoritative\"\nurl = \"u\"\n",
		"duplicate name":                          base + auth + "\n[[remotes]]\nname = \"line\"\nkind = \"storage\"\nurl = \"u\"\n",
		"missing name":                            base + auth + "\n[[remotes]]\nkind = \"storage\"\nurl = \"u\"\n",
		"credentials in url":                      base + auth + "\n[[remotes]]\nname = \"gh\"\nkind = \"storage\"\nurl = \"https://user:secret@github.com/o/r.git\"\n",
		"publish entry not a ref":                 base + auth + "\n[[remotes]]\nname = \"gh\"\nkind = \"storage\"\nurl = \"u\"\npublish = [\"master\"]\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(doc)); err == nil {
				t.Fatalf("accepted:\n%s", doc)
			}
		})
	}
}
