# Line Witness + Merge Guard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An agent can ask its agentshd witness to attest a commit and merge it; the witness runs the constitution's required checks in a clean, witness-owned checkout, signs a DSSE/in-toto attestation over the tree, pushes a candidate ref, and a merge guard verifies the attestation and fast-forwards master by compare-and-swap.

**Architecture:** A new Go module `line/` holds everything that is not agentsh-specific: `attest` (DSSE + in-toto + Ed25519 keys), `constitution` (v0 TOML rules), `gitx` (git plumbing), `guard` (merge guard + bare-repo hook), `witness` (attest + merge orchestration over a pluggable `Runner`), and a scenario harness that drives scripted fake agents end to end with no LLM. agentsh imports `line` through a `replace` directive and becomes the witness: a `witness` config section binds role and agent ID for the daemon's lifetime, checks run through agentsh's executor so every check leaves an invocation record as evidence, attestations are stored in Turso, and two RPC ops (`line_attest`, `line_merge`) plus CLI subcommands expose it. In v1 the guard runs in-process inside the witness against a local bare repository; separating it into its own service is later work.

**Tech Stack:** Go 1.24.9, stdlib `crypto/ed25519`, `github.com/pelletier/go-toml/v2` v2.4.3 (already used by depmaint), git CLI, agentsh's existing go-libsql store.

**Spec:** `design-docs/assembly-line-spec.md` (build order step 1; sections "Flow of a change", "Witness and trust", "Constitution", "Testing"). Principles: `design-docs/assembly-line-principles.md`.

## Global Constraints

- Module path for the new module: `github.com/Fewbytes/sweatshop/line`, directory `line/`, `go 1.24.9`.
- `line/` has no CGO and no dependency beyond the stdlib and `github.com/pelletier/go-toml/v2 v2.4.3`.
- agentsh references it with `require github.com/Fewbytes/sweatshop/line v0.0.0-00010101000000-000000000000` and `replace github.com/Fewbytes/sweatshop/line => ../line`.
- Attestations: in-toto Statement v1 (`_type` = `https://in-toto.io/Statement/v1`) inside a DSSE envelope, payload type `application/vnd.in-toto+json`, predicate type `https://github.com/Fewbytes/sweatshop/line/attest/checks/v0`, subject name `git-tree`, digest algorithm `gitTree`.
- Key ID = lowercase hex SHA-256 of the raw 32-byte Ed25519 public key. Private key file = base64 seed, mode 0600; public key file = `<key>.pub`, base64.
- Roles: `maker`, `andon`, `kaizen`, `gemba`.
- Refs: guarded branch default `refs/heads/master`; candidates `refs/line/candidates/<commit>`; andon stop flag `refs/line/andon` (exists = line stopped).
- Checks never run inside the agent's repository or container. The witness fetches from the agent repo into a witness-owned bare cache and checks out into a fresh directory.
- Constitution v0: `trust.level` is `none` or `attest`; `attest` requires `runner.kind = "container"`; `local` runner only at `none`. Signatures are verified at every trust level.
- The agentsh witness config must not live inside the workspace (agent-writable); nor may its key file, constitution, state dir, or remote.
- Conservative git policy for this repo: every task ends in a local commit; never push.
- Use `rg`/`fd`, not `grep`/`find`, in any shell work.

---

## File Structure

```
line/
  go.mod, go.sum
  attest/
    dsse.go            DSSE envelope, PAE, Sign, Verify
    keys.go            key IDs, key files, public key encoding
    statement.go       in-toto statement + checks predicate, SignStatement, VerifyStatement
    dsse_test.go, keys_test.go, statement_test.go
  constitution/
    constitution.go    v0 schema, Parse, Load (+digest), TrustedKeys, AllowedRoles, MatchPath, Runner.ID
    constitution_test.go
  gitx/
    git.go             Repo wrapper: Git, Commit, Tree, Parents, ChangedPaths, Ref, RefExists, UpdateRef (CAS), DeleteRef
    git_test.go
  guard/
    guard.go           Guard.Merge: verify attestation, rules, fast-forward CAS
    hooks.go           InstallHooks: pre-receive allowing only candidate refs
    guard_test.go, hooks_test.go
  witness/
    runner.go          Runner, ExecRunner, ContainerRunner, NewRunner
    witness.go         Witness.Attest: fetch → clean checkout → checks → sign
    merge.go           Witness.Merge: attest → push candidate → guard
    runner_test.go, witness_test.go, merge_test.go
  harness/
    harness_test.go    end-to-end scenarios with scripted fake agents
  cmd/sweatshop/
    main.go            keygen, guard install-hooks
    main_test.go
agentsh/
  go.mod                         + require/replace line
  internal/config/config.go      + Witness section and validation
  internal/config/witness_test.go
  internal/storage/store.go      + migration 2 (attestations)
  internal/storage/attestation.go  SaveAttestation, GetAttestation
  internal/storage/attestation_test.go
  internal/rpc/protocol.go       + OpLineAttest, OpLineMerge, LineRequest
  internal/daemon/witness.go     setupWitness, executorRunner, handlers
  internal/daemon/server.go      + fields, Serve hook, handler table entries
  internal/daemon/witness_test.go
  internal/daemon/ph2_test.go    + new ops in the coverage test
  cmd/agentsh/main.go            + attest, merge subcommands
  cmd/agentsh/line_test.go
justfile                         + line module in build/test/lint
README.md                        + line component
```

---

### Task 1: `attest` package — DSSE, keys, in-toto statements

**Files:**
- Create: `line/go.mod`
- Create: `line/attest/dsse.go`, `line/attest/keys.go`, `line/attest/statement.go`
- Test: `line/attest/dsse_test.go`, `line/attest/keys_test.go`, `line/attest/statement_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Signature struct{ KeyID, Sig string }`, `type Envelope struct{ PayloadType, Payload string; Signatures []Signature }` (JSON: `keyid`, `sig`, `payloadType`, `payload`, `signatures`)
  - `func PAE(payloadType string, payload []byte) []byte`
  - `func Sign(key ed25519.PrivateKey, payloadType string, payload []byte) Envelope`
  - `func Verify(env Envelope, trusted map[string]ed25519.PublicKey) ([]byte, string, error)`; `var ErrNoTrustedSignature`
  - `func KeyID(ed25519.PublicKey) string`, `func EncodePublicKey(ed25519.PublicKey) string`, `func ParsePublicKey(string) (ed25519.PublicKey, error)`, `func GenerateKeyFile(path string) (ed25519.PublicKey, error)`, `func LoadPrivateKey(path string) (ed25519.PrivateKey, error)`
  - consts `PayloadType`, `StatementType`, `ChecksPredicateType`, `TreeSubjectName`, `TreeDigestAlgorithm`
  - `type CheckResult struct{ Name, Command string; ExitCode int; EvidenceID string }`
  - `type ChecksPredicate struct{ Commit, Role, AgentID, Constitution, Runner string; Checks []CheckResult; FinishedAt time.Time }`
  - `type Statement struct{ Type string; Subject []Subject; PredicateType string; Predicate ChecksPredicate }`, `type Subject struct{ Name string; Digest map[string]string }`
  - `func NewStatement(tree string, p ChecksPredicate) Statement`, `func (Statement) Tree() (string, error)`
  - `func SignStatement(ed25519.PrivateKey, Statement) (Envelope, error)`, `func VerifyStatement(Envelope, map[string]ed25519.PublicKey) (Statement, string, error)`

- [ ] **Step 1: Create the module**

`line/go.mod`:
```
module github.com/Fewbytes/sweatshop/line

go 1.24.9
```

- [ ] **Step 2: Write failing DSSE tests**

`line/attest/dsse_test.go`:
```go
package attest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestPAEMatchesDSSESpecExample(t *testing.T) {
	got := string(PAE("http://example.com/HelloWorld", []byte("hello world")))
	want := "DSSEv1 29 http://example.com/HelloWorld 11 hello world"
	if got != want {
		t.Fatalf("PAE = %q, want %q", got, want)
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv := newKey(t)
	env := Sign(priv, "text/plain", []byte("payload"))
	payload, keyID, err := Verify(env, map[string]ed25519.PublicKey{KeyID(pub): pub})
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "payload" || keyID != KeyID(pub) {
		t.Fatalf("got payload %q key %q", payload, keyID)
	}
}

func TestVerifyRejectsUntrustedKey(t *testing.T) {
	_, priv := newKey(t)
	other, _ := newKey(t)
	env := Sign(priv, "text/plain", []byte("payload"))
	if _, _, err := Verify(env, map[string]ed25519.PublicKey{KeyID(other): other}); !errors.Is(err, ErrNoTrustedSignature) {
		t.Fatalf("err = %v, want ErrNoTrustedSignature", err)
	}
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	pub, priv := newKey(t)
	env := Sign(priv, "text/plain", []byte("payload"))
	env.Payload = base64.StdEncoding.EncodeToString([]byte("forged"))
	if _, _, err := Verify(env, map[string]ed25519.PublicKey{KeyID(pub): pub}); !errors.Is(err, ErrNoTrustedSignature) {
		t.Fatalf("err = %v, want ErrNoTrustedSignature", err)
	}
}

func TestVerifyRejectsTamperedPayloadType(t *testing.T) {
	pub, priv := newKey(t)
	env := Sign(priv, "text/plain", []byte("payload"))
	env.PayloadType = "application/other"
	if _, _, err := Verify(env, map[string]ed25519.PublicKey{KeyID(pub): pub}); !errors.Is(err, ErrNoTrustedSignature) {
		t.Fatalf("err = %v, want ErrNoTrustedSignature", err)
	}
}

func TestVerifyRejectsSpoofedKeyID(t *testing.T) {
	trustedPub, _ := newKey(t)
	_, attackerPriv := newKey(t)
	env := Sign(attackerPriv, "text/plain", []byte("payload"))
	env.Signatures[0].KeyID = KeyID(trustedPub)
	if _, _, err := Verify(env, map[string]ed25519.PublicKey{KeyID(trustedPub): trustedPub}); !errors.Is(err, ErrNoTrustedSignature) {
		t.Fatalf("err = %v, want ErrNoTrustedSignature", err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd line && go test ./attest/`
Expected: FAIL — `undefined: PAE`, `undefined: Sign`, etc.

- [ ] **Step 4: Implement DSSE and key ID**

`line/attest/dsse.go`:
```go
// Package attest signs and verifies DSSE envelopes carrying in-toto
// statements with Ed25519 keys.
package attest

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
)

type Signature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

type Envelope struct {
	PayloadType string      `json:"payloadType"`
	Payload     string      `json:"payload"`
	Signatures  []Signature `json:"signatures"`
}

var ErrNoTrustedSignature = errors.New("no valid signature from a trusted key")

// PAE is the DSSE pre-authentication encoding. Signatures cover it rather than
// the raw payload, so the payload type cannot be swapped under a signature.
func PAE(payloadType string, payload []byte) []byte {
	return []byte(fmt.Sprintf("DSSEv1 %d %s %d %s", len(payloadType), payloadType, len(payload), payload))
}

func Sign(key ed25519.PrivateKey, payloadType string, payload []byte) Envelope {
	pub := key.Public().(ed25519.PublicKey)
	sig := ed25519.Sign(key, PAE(payloadType, payload))
	return Envelope{
		PayloadType: payloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures:  []Signature{{KeyID: KeyID(pub), Sig: base64.StdEncoding.EncodeToString(sig)}},
	}
}

// Verify returns the payload and the ID of the first trusted key whose
// signature checks out.
func Verify(env Envelope, trusted map[string]ed25519.PublicKey) ([]byte, string, error) {
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return nil, "", fmt.Errorf("decode payload: %w", err)
	}
	message := PAE(env.PayloadType, payload)
	for _, s := range env.Signatures {
		pub, ok := trusted[s.KeyID]
		if !ok {
			continue
		}
		sig, err := base64.StdEncoding.DecodeString(s.Sig)
		if err != nil {
			continue
		}
		if ed25519.Verify(pub, message, sig) {
			return payload, s.KeyID, nil
		}
	}
	return nil, "", ErrNoTrustedSignature
}
```

`line/attest/keys.go` (the DSSE code needs only `KeyID`; the whole file goes in now and Step 6 tests the rest):
```go
package attest

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

func EncodePublicKey(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

func ParsePublicKey(encoded string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("decode public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// GenerateKeyFile writes a new private key seed to path (mode 0600, never
// overwriting) and the public key to path+".pub".
func GenerateKeyFile(path string) (ed25519.PublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(priv.Seed()) + "\n"); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path+".pub", []byte(EncodePublicKey(pub)+"\n"), 0o644); err != nil {
		return nil, err
	}
	return pub, nil
}

// LoadPrivateKey refuses a key file readable by group or others.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("%s is mode %#o; a signing key must be 0600", path, perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("decode key %s: %w", path, err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("key file does not hold an ed25519 seed")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
```

- [ ] **Step 5: Run DSSE tests**

Run: `cd line && go test ./attest/`
Expected: PASS.

- [ ] **Step 6: Write failing key-file and statement tests**

`line/attest/keys_test.go`:
```go
package attest

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateAndLoadKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "witness.key")
	pub, err := GenerateKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := LoadPrivateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !priv.Public().(ed25519.PublicKey).Equal(pub) {
		t.Fatal("loaded key does not match generated public key")
	}
	encoded, err := os.ReadFile(path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParsePublicKey(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Equal(pub) {
		t.Fatal(".pub file does not match generated key")
	}
}

func TestGenerateKeyFileNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "witness.key")
	if _, err := GenerateKeyFile(path); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateKeyFile(path); err == nil {
		t.Fatal("second GenerateKeyFile overwrote an existing key")
	}
}

func TestLoadPrivateKeyRejectsReadableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "witness.key")
	if _, err := GenerateKeyFile(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrivateKey(path); err == nil {
		t.Fatal("loaded a world-readable signing key")
	}
}

func TestParsePublicKeyRejectsWrongLength(t *testing.T) {
	if _, err := ParsePublicKey("AAAA"); err == nil {
		t.Fatal("accepted a 3-byte public key")
	}
}
```

`line/attest/statement_test.go`:
```go
package attest

import (
	"crypto/ed25519"
	"testing"
	"time"
)

func TestStatementSignVerifyRoundTrip(t *testing.T) {
	pub, priv := newKey(t)
	statement := NewStatement("abc123", ChecksPredicate{
		Commit: "def456", Role: "maker", AgentID: "maker-1", Constitution: "sha256:x", Runner: "local",
		Checks:     []CheckResult{{Name: "ci", Command: "just ci", ExitCode: 0, EvidenceID: "inv_1"}},
		FinishedAt: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC),
	})
	env, err := SignStatement(priv, statement)
	if err != nil {
		t.Fatal(err)
	}
	got, keyID, err := VerifyStatement(env, map[string]ed25519.PublicKey{KeyID(pub): pub})
	if err != nil {
		t.Fatal(err)
	}
	if keyID != KeyID(pub) {
		t.Fatalf("keyID = %s", keyID)
	}
	tree, err := got.Tree()
	if err != nil || tree != "abc123" {
		t.Fatalf("Tree() = %q, %v", tree, err)
	}
	if got.Predicate.Checks[0].EvidenceID != "inv_1" || got.Predicate.Role != "maker" {
		t.Fatalf("predicate did not round-trip: %+v", got.Predicate)
	}
}

func TestVerifyStatementRejectsWrongPayloadType(t *testing.T) {
	pub, priv := newKey(t)
	env := Sign(priv, "text/plain", []byte(`{}`))
	if _, _, err := VerifyStatement(env, map[string]ed25519.PublicKey{KeyID(pub): pub}); err == nil {
		t.Fatal("accepted a non-in-toto payload type")
	}
}

func TestVerifyStatementRejectsForeignPredicate(t *testing.T) {
	pub, priv := newKey(t)
	statement := NewStatement("abc", ChecksPredicate{})
	statement.PredicateType = "https://slsa.dev/provenance/v1"
	env, err := SignStatement(priv, statement)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := VerifyStatement(env, map[string]ed25519.PublicKey{KeyID(pub): pub}); err == nil {
		t.Fatal("accepted a statement with a foreign predicate type")
	}
}

func TestTreeRequiresGitTreeSubject(t *testing.T) {
	statement := Statement{Subject: []Subject{{Name: "file", Digest: map[string]string{"sha256": "x"}}}}
	if _, err := statement.Tree(); err == nil {
		t.Fatal("Tree() found a tree in a statement without one")
	}
}
```

- [ ] **Step 7: Run tests to verify the statement tests fail**

Run: `cd line && go test ./attest/`
Expected: FAIL — `undefined: NewStatement`, `undefined: ChecksPredicate`, etc. (key tests already pass).

- [ ] **Step 8: Implement statements**

`line/attest/statement.go`:
```go
package attest

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	PayloadType         = "application/vnd.in-toto+json"
	StatementType       = "https://in-toto.io/Statement/v1"
	ChecksPredicateType = "https://github.com/Fewbytes/sweatshop/line/attest/checks/v0"
	TreeSubjectName     = "git-tree"
	TreeDigestAlgorithm = "gitTree"
)

type Subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

type CheckResult struct {
	Name       string `json:"name"`
	Command    string `json:"command"`
	ExitCode   int    `json:"exit_code"`
	EvidenceID string `json:"evidence_id,omitempty"`
}

type ChecksPredicate struct {
	Commit       string        `json:"commit"`
	Role         string        `json:"role"`
	AgentID      string        `json:"agent_id"`
	Constitution string        `json:"constitution"`
	Runner       string        `json:"runner"`
	Checks       []CheckResult `json:"checks"`
	FinishedAt   time.Time     `json:"finished_at"`
}

type Statement struct {
	Type          string          `json:"_type"`
	Subject       []Subject       `json:"subject"`
	PredicateType string          `json:"predicateType"`
	Predicate     ChecksPredicate `json:"predicate"`
}

func NewStatement(tree string, predicate ChecksPredicate) Statement {
	return Statement{
		Type:          StatementType,
		Subject:       []Subject{{Name: TreeSubjectName, Digest: map[string]string{TreeDigestAlgorithm: tree}}},
		PredicateType: ChecksPredicateType,
		Predicate:     predicate,
	}
}

func (s Statement) Tree() (string, error) {
	for _, subject := range s.Subject {
		if subject.Name == TreeSubjectName && subject.Digest[TreeDigestAlgorithm] != "" {
			return subject.Digest[TreeDigestAlgorithm], nil
		}
	}
	return "", errors.New("statement has no git-tree subject")
}

func SignStatement(key ed25519.PrivateKey, statement Statement) (Envelope, error) {
	payload, err := json.Marshal(statement)
	if err != nil {
		return Envelope{}, err
	}
	return Sign(key, PayloadType, payload), nil
}

func VerifyStatement(env Envelope, trusted map[string]ed25519.PublicKey) (Statement, string, error) {
	if env.PayloadType != PayloadType {
		return Statement{}, "", fmt.Errorf("payload type %q, want %q", env.PayloadType, PayloadType)
	}
	payload, keyID, err := Verify(env, trusted)
	if err != nil {
		return Statement{}, "", err
	}
	var statement Statement
	if err := json.Unmarshal(payload, &statement); err != nil {
		return Statement{}, "", fmt.Errorf("decode statement: %w", err)
	}
	if statement.Type != StatementType || statement.PredicateType != ChecksPredicateType {
		return Statement{}, "", fmt.Errorf("unexpected statement %q with predicate %q", statement.Type, statement.PredicateType)
	}
	return statement, keyID, nil
}
```

- [ ] **Step 9: Run all attest tests, vet, gofmt**

Run: `cd line && go test ./attest/ && go vet ./... && test -z "$(gofmt -l .)"`
Expected: PASS, no vet output, no gofmt output.

- [ ] **Step 10: Commit**

```bash
git add line/go.mod line/attest
git commit -m "feat(line): DSSE/in-toto attestations with Ed25519 keys"
```

---

### Task 2: `constitution` package — v0 rules

**Files:**
- Modify: `line/go.mod` (add go-toml)
- Create: `line/constitution/constitution.go`
- Test: `line/constitution/constitution_test.go`

**Interfaces:**
- Consumes: `attest.ParsePublicKey`, `attest.KeyID`.
- Produces:
  - `type Constitution struct{ Trust Trust; Checks Checks; Runner Runner; Guards Guards }`
  - `type Trust struct{ Level string; Keys []TrustedKey }`, `type TrustedKey struct{ Name, PublicKey string }`
  - `type Checks struct{ Required []Check }`, `type Check struct{ Name, Command string }`
  - `type Runner struct{ Kind, Engine, Image string }`, `func (Runner) ID() string` → `"local"` or `"container:<engine>:<image>"`
  - `type Guards struct{ Paths []PathGuard }`, `type PathGuard struct{ Pattern string; Roles []string }`
  - `var Roles = []string{"maker", "andon", "kaizen", "gemba"}`
  - `func Parse(data []byte) (Constitution, error)`, `func Load(path string) (Constitution, string, error)` (digest `"sha256:<hex of file bytes>"`)
  - `func (Constitution) TrustedKeys() (map[string]ed25519.PublicKey, error)`
  - `func (Constitution) AllowedRoles(path string) ([]string, bool)`
  - `func MatchPath(pattern, path string) bool`

- [ ] **Step 1: Write failing tests**

`line/constitution/constitution_test.go`:
```go
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

func TestLoadDigestTracksContent(t *testing.T) {
	_, encoded := publicKey(t)
	dir := t.TempDir()
	a := filepath.Join(dir, "a.toml")
	b := filepath.Join(dir, "b.toml")
	os.WriteFile(a, []byte(valid(encoded)), 0o644)
	os.WriteFile(b, []byte(valid(encoded)+"\n# amended\n"), 0o644)
	_, digestA, err := Load(a)
	if err != nil {
		t.Fatal(err)
	}
	_, digestA2, _ := Load(a)
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
```

- [ ] **Step 2: Run to verify failure**

Run: `cd line && go test ./constitution/`
Expected: FAIL — `undefined: Parse`, `undefined: Load`, etc.

- [ ] **Step 3: Add the TOML dependency**

Run: `cd line && go get github.com/pelletier/go-toml/v2@v2.4.3`
Expected: `go.mod` gains `require github.com/pelletier/go-toml/v2 v2.4.3`; `go.sum` created.

- [ ] **Step 4: Implement**

`line/constitution/constitution.go`:
```go
// Package constitution loads the line's rules. v0 holds only what the witness
// and merge guard need: trusted witness keys, required checks, the check
// runner, and role-gated paths.
package constitution

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/pelletier/go-toml/v2"
)

var Roles = []string{"maker", "andon", "kaizen", "gemba"}

type Constitution struct {
	Trust  Trust  `toml:"trust"`
	Checks Checks `toml:"checks"`
	Runner Runner `toml:"runner"`
	Guards Guards `toml:"guards"`
}

// Trust.Level labels how far check environments can be trusted. Signatures
// are verified at every level; "none" only permits host-local check runs.
type Trust struct {
	Level string       `toml:"level"`
	Keys  []TrustedKey `toml:"keys"`
}

type TrustedKey struct {
	Name      string `toml:"name"`
	PublicKey string `toml:"public_key"`
}

type Checks struct {
	Required []Check `toml:"required"`
}

type Check struct {
	Name    string `toml:"name"`
	Command string `toml:"command"`
}

type Runner struct {
	Kind   string `toml:"kind"`
	Engine string `toml:"engine"`
	Image  string `toml:"image"`
}

// ID is recorded in attestations so the guard can reject checks that ran in
// a different environment than the constitution demands.
func (r Runner) ID() string {
	if r.Kind == "container" {
		return "container:" + r.Engine + ":" + r.Image
	}
	return "local"
}

type Guards struct {
	Paths []PathGuard `toml:"paths"`
}

type PathGuard struct {
	Pattern string   `toml:"pattern"`
	Roles   []string `toml:"roles"`
}

func Load(file string) (Constitution, string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return Constitution{}, "", err
	}
	c, err := Parse(data)
	if err != nil {
		return Constitution{}, "", fmt.Errorf("%s: %w", file, err)
	}
	sum := sha256.Sum256(data)
	return c, "sha256:" + hex.EncodeToString(sum[:]), nil
}

func Parse(data []byte) (Constitution, error) {
	var c Constitution
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return Constitution{}, fmt.Errorf("parse constitution: %w", err)
	}
	if c.Runner.Kind == "container" && c.Runner.Engine == "" {
		c.Runner.Engine = "docker"
	}
	if err := c.validate(); err != nil {
		return Constitution{}, err
	}
	return c, nil
}

func (c Constitution) validate() error {
	switch c.Runner.Kind {
	case "local":
	case "container":
		if c.Runner.Image == "" {
			return errors.New("runner kind container requires runner.image")
		}
	default:
		return fmt.Errorf("runner.kind %q: want local or container", c.Runner.Kind)
	}
	switch c.Trust.Level {
	case "none":
	case "attest":
		if c.Runner.Kind != "container" {
			return errors.New("trust level attest requires runner kind container")
		}
	default:
		return fmt.Errorf("trust.level %q: want none or attest", c.Trust.Level)
	}
	if len(c.Checks.Required) == 0 {
		return errors.New("checks.required is empty; the merge guard would accept anything")
	}
	seen := map[string]bool{}
	for _, check := range c.Checks.Required {
		if check.Name == "" || check.Command == "" {
			return errors.New("every required check needs a name and a command")
		}
		if seen[check.Name] {
			return fmt.Errorf("duplicate required check %q", check.Name)
		}
		seen[check.Name] = true
	}
	if _, err := c.TrustedKeys(); err != nil {
		return err
	}
	for _, g := range c.Guards.Paths {
		if g.Pattern == "" || len(g.Roles) == 0 {
			return errors.New("every path guard needs a pattern and at least one role")
		}
		for _, role := range g.Roles {
			if !slices.Contains(Roles, role) {
				return fmt.Errorf("path guard %q: unknown role %q", g.Pattern, role)
			}
		}
	}
	return nil
}

func (c Constitution) TrustedKeys() (map[string]ed25519.PublicKey, error) {
	keys := make(map[string]ed25519.PublicKey, len(c.Trust.Keys))
	for _, k := range c.Trust.Keys {
		pub, err := attest.ParsePublicKey(k.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("trust key %q: %w", k.Name, err)
		}
		keys[attest.KeyID(pub)] = pub
	}
	return keys, nil
}

// AllowedRoles returns the roles permitted to change p, and whether any path
// guard covers it. The first matching guard wins.
func (c Constitution) AllowedRoles(p string) ([]string, bool) {
	for _, g := range c.Guards.Paths {
		if MatchPath(g.Pattern, p) {
			return g.Roles, true
		}
	}
	return nil, false
}

// MatchPath matches slash-separated repository paths with path.Match; a
// trailing "/**" matches a directory and everything beneath it.
func MatchPath(pattern, p string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "/**"); ok {
		return p == prefix || strings.HasPrefix(p, prefix+"/")
	}
	ok, err := path.Match(pattern, p)
	return err == nil && ok
}
```

- [ ] **Step 5: Run tests**

Run: `cd line && go test ./constitution/ && go vet ./... && test -z "$(gofmt -l .)"`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add line/go.mod line/go.sum line/constitution
git commit -m "feat(line): constitution v0 — trusted keys, required checks, runner, path guards"
```

---

### Task 3: `gitx` package — git plumbing with compare-and-swap

**Files:**
- Create: `line/gitx/git.go`
- Test: `line/gitx/git_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Repo struct{ Dir string }`
  - `func (Repo) Git(ctx context.Context, args ...string) (string, error)` — trimmed stdout; errors are `*Error`
  - `type Error struct{ Args []string; Stderr string; Err error }`
  - `var ErrRefMoved = errors.New("ref moved")`
  - `func (Repo) Commit(ctx, rev string) (string, error)`, `Tree(ctx, commit) (string, error)`, `Parents(ctx, commit) ([]string, error)`, `ChangedPaths(ctx, from, to string) ([]string, error)`, `Ref(ctx, ref) (string, error)`, `RefExists(ctx, ref) (bool, error)`, `UpdateRef(ctx, ref, newValue, oldValue string) error` (returns `ErrRefMoved` when the ref no longer holds `oldValue`), `DeleteRef(ctx, ref) error`

- [ ] **Step 1: Write failing tests**

`line/gitx/git_test.go`:
```go
package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newRepo(t *testing.T) (Repo, context.Context) {
	t.Helper()
	ctx := context.Background()
	r := Repo{Dir: t.TempDir()}
	for _, args := range [][]string{
		{"init", "--quiet", "--initial-branch=master"},
		{"config", "user.name", "test"},
		{"config", "user.email", "test@example.com"},
	} {
		if _, err := r.Git(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	return r, ctx
}

func commitFile(t *testing.T, r Repo, ctx context.Context, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(r.Dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Git(ctx, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Git(ctx, "commit", "--quiet", "-m", "change "+name); err != nil {
		t.Fatal(err)
	}
	sha, err := r.Commit(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

func TestCommitTreeParents(t *testing.T) {
	r, ctx := newRepo(t)
	first := commitFile(t, r, ctx, "a.txt", "1")
	second := commitFile(t, r, ctx, "b.txt", "2")
	parents, err := r.Parents(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if len(parents) != 1 || parents[0] != first {
		t.Fatalf("Parents = %v, want [%s]", parents, first)
	}
	if roots, _ := r.Parents(ctx, first); len(roots) != 0 {
		t.Fatalf("root commit has parents %v", roots)
	}
	tree, err := r.Tree(ctx, second)
	if err != nil || len(tree) != 40 {
		t.Fatalf("Tree = %q, %v", tree, err)
	}
	if _, err := r.Commit(ctx, "no-such-branch"); err == nil {
		t.Fatal("Commit resolved a missing revision")
	}
}

func TestChangedPaths(t *testing.T) {
	r, ctx := newRepo(t)
	first := commitFile(t, r, ctx, "a.txt", "1")
	commitFile(t, r, ctx, "dir/b.txt", "2")
	last := commitFile(t, r, ctx, "a.txt", "3")
	paths, err := r.ChangedPaths(ctx, first, last)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "a.txt" || paths[1] != "dir/b.txt" {
		t.Fatalf("ChangedPaths = %v", paths)
	}
	if none, _ := r.ChangedPaths(ctx, last, last); len(none) != 0 {
		t.Fatalf("ChangedPaths(x, x) = %v", none)
	}
}

func TestRefExists(t *testing.T) {
	r, ctx := newRepo(t)
	head := commitFile(t, r, ctx, "a.txt", "1")
	if ok, err := r.RefExists(ctx, "refs/line/andon"); err != nil || ok {
		t.Fatalf("RefExists(missing) = %v, %v", ok, err)
	}
	if _, err := r.Git(ctx, "update-ref", "refs/line/andon", head); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.RefExists(ctx, "refs/line/andon"); err != nil || !ok {
		t.Fatalf("RefExists(present) = %v, %v", ok, err)
	}
}

func TestUpdateRefCompareAndSwap(t *testing.T) {
	r, ctx := newRepo(t)
	first := commitFile(t, r, ctx, "a.txt", "1")
	second := commitFile(t, r, ctx, "a.txt", "2")
	third := commitFile(t, r, ctx, "a.txt", "3")
	if err := r.UpdateRef(ctx, "refs/heads/target", first, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateRef(ctx, "refs/heads/target", second, first); err != nil {
		t.Fatal(err)
	}
	err := r.UpdateRef(ctx, "refs/heads/target", third, first)
	if !errors.Is(err, ErrRefMoved) {
		t.Fatalf("stale CAS err = %v, want ErrRefMoved", err)
	}
	if got, _ := r.Ref(ctx, "refs/heads/target"); got != second {
		t.Fatalf("ref = %s after failed CAS, want %s", got, second)
	}
}

func TestDeleteRef(t *testing.T) {
	r, ctx := newRepo(t)
	head := commitFile(t, r, ctx, "a.txt", "1")
	r.UpdateRef(ctx, "refs/line/candidates/x", head, "")
	if err := r.DeleteRef(ctx, "refs/line/candidates/x"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.RefExists(ctx, "refs/line/candidates/x"); ok {
		t.Fatal("ref still exists after DeleteRef")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd line && go test ./gitx/`
Expected: FAIL — `undefined: Repo`.

- [ ] **Step 3: Implement**

`line/gitx/git.go`:
```go
// Package gitx runs git for line components. Calls never prompt and never
// read the invoking user's global or system config, so a witness or guard
// behaves the same regardless of whose account it runs under.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var ErrRefMoved = errors.New("ref moved")

type Repo struct {
	Dir string
}

type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.Args, " "), e.Err, e.Stderr)
}

func (e *Error) Unwrap() error { return e.Err }

func (r Repo) Git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", r.Dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", &Error{Args: args, Stderr: strings.TrimSpace(stderr.String()), Err: err}
	}
	return strings.TrimSpace(stdout.String()), nil
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func (r Repo) Commit(ctx context.Context, rev string) (string, error) {
	return r.Git(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
}

func (r Repo) Tree(ctx context.Context, commit string) (string, error) {
	return r.Git(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", commit+"^{tree}")
}

func (r Repo) Parents(ctx context.Context, commit string) ([]string, error) {
	out, err := r.Git(ctx, "rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return nil, fmt.Errorf("commit %s not found", commit)
	}
	return fields[1:], nil
}

func (r Repo) ChangedPaths(ctx context.Context, from, to string) ([]string, error) {
	out, err := r.Git(ctx, "diff", "--name-only", "--no-renames", "-z", from, to)
	if err != nil {
		return nil, err
	}
	out = strings.TrimRight(out, "\x00")
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\x00"), nil
}

func (r Repo) Ref(ctx context.Context, ref string) (string, error) {
	return r.Git(ctx, "rev-parse", "--verify", "--quiet", ref)
}

func (r Repo) RefExists(ctx context.Context, ref string) (bool, error) {
	_, err := r.Git(ctx, "show-ref", "--verify", "--quiet", ref)
	if err == nil {
		return true, nil
	}
	if exitCode(err) == 1 {
		return false, nil
	}
	return false, err
}

// UpdateRef moves ref to newValue only if it currently holds oldValue (an
// empty oldValue means the ref must not exist). git performs the
// compare-and-swap under the ref lock.
func (r Repo) UpdateRef(ctx context.Context, ref, newValue, oldValue string) error {
	_, err := r.Git(ctx, "update-ref", ref, newValue, oldValue)
	if err == nil {
		return nil
	}
	current, readErr := r.Ref(ctx, ref)
	if readErr == nil && current != oldValue {
		return ErrRefMoved
	}
	return err
}

func (r Repo) DeleteRef(ctx context.Context, ref string) error {
	_, err := r.Git(ctx, "update-ref", "-d", ref)
	return err
}
```

Note: `--end-of-options` stops an agent-supplied revision that starts with `-` from being parsed as an option.

- [ ] **Step 4: Run tests**

Run: `cd line && go test ./gitx/ && go vet ./... && test -z "$(gofmt -l .)"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add line/gitx
git commit -m "feat(line): gitx — git plumbing with compare-and-swap ref updates"
```

---

### Task 4: `guard` package — merge guard and bare-repo hook

**Files:**
- Create: `line/guard/guard.go`, `line/guard/hooks.go`
- Test: `line/guard/guard_test.go`, `line/guard/hooks_test.go`

**Interfaces:**
- Consumes: `attest.VerifyStatement`, `attest.Envelope`, `attest.CheckResult`; `constitution.Constitution`, `constitution.Check`, `(Constitution).TrustedKeys`, `(Constitution).AllowedRoles`, `(Runner).ID`; all `gitx.Repo` methods, `gitx.ErrRefMoved`.
- Produces:
  - consts `StopRef = "refs/line/andon"`, `CandidatePrefix = "refs/line/candidates/"`
  - `type Status string` with `Merged`, `Rejected`
  - `type Reason string` with `ReasonUntrusted = "untrusted_attestation"`, `ReasonTreeMismatch = "tree_mismatch"`, `ReasonConstitution = "constitution_mismatch"`, `ReasonRunner = "runner_mismatch"`, `ReasonChecksMissing = "checks_missing"`, `ReasonChecksFailed = "checks_failed"`, `ReasonLineStopped = "line_stopped"`, `ReasonBaseMoved = "base_moved"`, `ReasonRoleForbidden = "role_forbidden"`
  - `type Result struct{ Status Status; Reason Reason; Detail, Commit, Base string }` (JSON `status`, `reason`, `detail`, `commit`, `base`)
  - `type Guard struct{ Repo gitx.Repo; Ref string; Constitution constitution.Constitution; ConstitutionDigest string }`
  - `func (Guard) Merge(ctx context.Context, commit string, env attest.Envelope) (Result, error)` — error only for infrastructure failures; every rule violation is a `Rejected` result. Deletes the candidate ref when done.
  - `func InstallHooks(repoDir string) error`

- [ ] **Step 1: Write failing guard tests**

`line/guard/guard_test.go`:
```go
package guard

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
)

const digest = "sha256:test"

type fixture struct {
	t      *testing.T
	ctx    context.Context
	remote gitx.Repo
	work   gitx.Repo
	key    ed25519.PrivateKey
	guard  Guard
}

// ok checks a git call: ok(t)(repo.Git(...)). Go only lets a multi-value call
// fill an argument list on its own, hence the curried form.
func ok(t *testing.T) func(string, error) {
	return func(_ string, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	remote := gitx.Repo{Dir: filepath.Join(dir, "remote.git")}
	work := gitx.Repo{Dir: filepath.Join(dir, "work")}
	root := gitx.Repo{Dir: dir}
	ok(t)(root.Git(ctx, "init", "--quiet", "--bare", "--initial-branch=master", remote.Dir))
	ok(t)(root.Git(ctx, "clone", "--quiet", remote.Dir, work.Dir))
	ok(t)(work.Git(ctx, "symbolic-ref", "HEAD", "refs/heads/master"))
	ok(t)(work.Git(ctx, "config", "user.name", "test"))
	ok(t)(work.Git(ctx, "config", "user.email", "test@example.com"))
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, ctx: ctx, remote: remote, work: work, key: key}
	seed := f.commit("README", "seed")
	ok(t)(work.Git(ctx, "push", "--quiet", "origin", seed+":refs/heads/master"))
	f.guard = Guard{
		Repo: remote, Ref: "refs/heads/master", ConstitutionDigest: digest,
		Constitution: constitution.Constitution{
			Trust:  constitution.Trust{Level: "none", Keys: []constitution.TrustedKey{{Name: "w", PublicKey: attest.EncodePublicKey(pub)}}},
			Checks: constitution.Checks{Required: []constitution.Check{{Name: "ci", Command: "sh check.sh"}}},
			Runner: constitution.Runner{Kind: "local"},
			Guards: constitution.Guards{Paths: []constitution.PathGuard{{Pattern: "acceptance/**", Roles: []string{"gemba"}}}},
		},
	}
	return f
}

func (f *fixture) commit(name, content string) string {
	f.t.Helper()
	full := filepath.Join(f.work.Dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	ok(f.t)(f.work.Git(f.ctx, "add", "-A"))
	ok(f.t)(f.work.Git(f.ctx, "commit", "--quiet", "-m", "change "+name))
	sha, err := f.work.Commit(f.ctx, "HEAD")
	if err != nil {
		f.t.Fatal(err)
	}
	return sha
}

func (f *fixture) candidate(commit string) {
	f.t.Helper()
	ok(f.t)(f.work.Git(f.ctx, "push", "--quiet", "origin", commit+":"+CandidatePrefix+commit))
}

func (f *fixture) envelope(commit string, key ed25519.PrivateKey, mutate func(*attest.ChecksPredicate)) attest.Envelope {
	f.t.Helper()
	tree, err := f.work.Tree(f.ctx, commit)
	if err != nil {
		f.t.Fatal(err)
	}
	predicate := attest.ChecksPredicate{
		Commit: commit, Role: "maker", AgentID: "maker-1", Constitution: digest, Runner: "local",
		Checks:     []attest.CheckResult{{Name: "ci", Command: "sh check.sh", ExitCode: 0, EvidenceID: "inv_1"}},
		FinishedAt: time.Now().UTC(),
	}
	if mutate != nil {
		mutate(&predicate)
	}
	env, err := attest.SignStatement(key, attest.NewStatement(tree, predicate))
	if err != nil {
		f.t.Fatal(err)
	}
	return env
}

func (f *fixture) master() string {
	f.t.Helper()
	sha, err := f.remote.Ref(f.ctx, "refs/heads/master")
	if err != nil {
		f.t.Fatal(err)
	}
	return sha
}

func (f *fixture) expectRejected(commit string, env attest.Envelope, reason Reason) {
	f.t.Helper()
	before := f.master()
	result, err := f.guard.Merge(f.ctx, commit, env)
	if err != nil {
		f.t.Fatal(err)
	}
	if result.Status != Rejected || result.Reason != reason {
		f.t.Fatalf("result = %+v, want rejected/%s", result, reason)
	}
	if f.master() != before {
		f.t.Fatal("master moved on a rejected merge")
	}
}

func TestMergeFastForwardsMaster(t *testing.T) {
	f := newFixture(t)
	base := f.master()
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	result, err := f.guard.Merge(f.ctx, commit, f.envelope(commit, f.key, nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != Merged || result.Base != base || f.master() != commit {
		t.Fatalf("result = %+v, master = %s", result, f.master())
	}
	if ok, _ := f.remote.RefExists(f.ctx, CandidatePrefix+commit); ok {
		t.Fatal("candidate ref left behind after merge")
	}
}

func TestRejectsUntrustedKey(t *testing.T) {
	f := newFixture(t)
	_, attacker, _ := ed25519.GenerateKey(rand.Reader)
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	f.expectRejected(commit, f.envelope(commit, attacker, nil), ReasonUntrusted)
}

func TestRejectsAttestationForAnotherTree(t *testing.T) {
	f := newFixture(t)
	honest := f.commit("src/a.go", "package a")
	sneaky := f.commit("src/b.go", "package b // unattested")
	f.candidate(sneaky)
	f.expectRejected(sneaky, f.envelope(honest, f.key, nil), ReasonTreeMismatch)
}

func TestRejectsOtherConstitution(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	f.expectRejected(commit, f.envelope(commit, f.key, func(p *attest.ChecksPredicate) { p.Constitution = "sha256:old" }), ReasonConstitution)
}

func TestRejectsOtherRunner(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	f.expectRejected(commit, f.envelope(commit, f.key, func(p *attest.ChecksPredicate) { p.Runner = "container:docker:x" }), ReasonRunner)
}

func TestRejectsMissingCheck(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	f.expectRejected(commit, f.envelope(commit, f.key, func(p *attest.ChecksPredicate) { p.Checks = nil }), ReasonChecksMissing)
}

func TestRejectsCheckWithDifferentCommand(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	f.expectRejected(commit, f.envelope(commit, f.key, func(p *attest.ChecksPredicate) { p.Checks[0].Command = "true" }), ReasonChecksMissing)
}

func TestRejectsFailedCheckEvenWhenSigned(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	f.expectRejected(commit, f.envelope(commit, f.key, func(p *attest.ChecksPredicate) { p.Checks[0].ExitCode = 1 }), ReasonChecksFailed)
}

func TestRejectsWhenLineStopped(t *testing.T) {
	f := newFixture(t)
	ok(t)(f.remote.Git(f.ctx, "update-ref", StopRef, f.master()))
	commit := f.commit("src/a.go", "package a")
	f.candidate(commit)
	f.expectRejected(commit, f.envelope(commit, f.key, nil), ReasonLineStopped)
}

func TestRejectsStaleBase(t *testing.T) {
	f := newFixture(t)
	seed := f.master()
	first := f.commit("src/a.go", "package a")
	f.candidate(first)
	if r, err := f.guard.Merge(f.ctx, first, f.envelope(first, f.key, nil)); err != nil || r.Status != Merged {
		t.Fatalf("first merge: %+v %v", r, err)
	}
	ok(t)(f.work.Git(f.ctx, "reset", "--quiet", "--hard", seed))
	stale := f.commit("src/b.go", "package b")
	f.candidate(stale)
	result, err := f.guard.Merge(f.ctx, stale, f.envelope(stale, f.key, nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != Rejected || result.Reason != ReasonBaseMoved || result.Base != first {
		t.Fatalf("result = %+v", result)
	}
}

func TestRolesGatePaths(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("acceptance/login_test.go", "package acceptance")
	f.candidate(commit)
	f.expectRejected(commit, f.envelope(commit, f.key, nil), ReasonRoleForbidden)

	f.candidate(commit)
	result, err := f.guard.Merge(f.ctx, commit, f.envelope(commit, f.key, func(p *attest.ChecksPredicate) { p.Role = "gemba" }))
	if err != nil || result.Status != Merged {
		t.Fatalf("gemba merge: %+v %v", result, err)
	}
}
```

`line/guard/hooks_test.go`:
```go
package guard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/gitx"
)

func TestHookRejectsBranchPushesAndAllowsCandidates(t *testing.T) {
	f := newFixture(t)
	if err := InstallHooks(f.remote.Dir); err != nil {
		t.Fatal(err)
	}
	commit := f.commit("src/a.go", "package a")
	_, err := f.work.Git(f.ctx, "push", "origin", commit+":refs/heads/master")
	if err == nil || !strings.Contains(err.Error(), "submit through the witness") {
		t.Fatalf("direct push to master: err = %v", err)
	}
	if _, err := f.work.Git(f.ctx, "push", "--quiet", "origin", commit+":"+CandidatePrefix+commit); err != nil {
		t.Fatalf("candidate push rejected: %v", err)
	}
}

func TestInstallHooksIsIdempotentAndRefusesForeignHook(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "remote.git")
	if _, err := (gitx.Repo{Dir: filepath.Dir(dir)}).Git(ctx, "init", "--quiet", "--bare", dir); err != nil {
		t.Fatal(err)
	}
	if err := InstallHooks(dir); err != nil {
		t.Fatal(err)
	}
	if err := InstallHooks(dir); err != nil {
		t.Fatalf("second install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hooks", "pre-receive"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InstallHooks(dir); err == nil {
		t.Fatal("overwrote a foreign pre-receive hook")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd line && go test ./guard/`
Expected: FAIL — `undefined: Guard`, `undefined: InstallHooks`, etc.

- [ ] **Step 3: Implement the guard**

`line/guard/guard.go`:
```go
// Package guard is the merge guard: the only writer of the guarded branch.
// It accepts a candidate commit only with a trusted attestation that matches
// the commit's tree, the current constitution, and the required checks, and
// only as a fast-forward of the branch, applied by compare-and-swap.
package guard

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
)

const (
	StopRef         = "refs/line/andon"
	CandidatePrefix = "refs/line/candidates/"
)

type Status string

const (
	Merged   Status = "merged"
	Rejected Status = "rejected"
)

type Reason string

const (
	ReasonUntrusted     Reason = "untrusted_attestation"
	ReasonTreeMismatch  Reason = "tree_mismatch"
	ReasonConstitution  Reason = "constitution_mismatch"
	ReasonRunner        Reason = "runner_mismatch"
	ReasonChecksMissing Reason = "checks_missing"
	ReasonChecksFailed  Reason = "checks_failed"
	ReasonLineStopped   Reason = "line_stopped"
	ReasonBaseMoved     Reason = "base_moved"
	ReasonRoleForbidden Reason = "role_forbidden"
)

type Result struct {
	Status Status `json:"status"`
	Reason Reason `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
	Commit string `json:"commit"`
	Base   string `json:"base,omitempty"`
}

type Guard struct {
	Repo               gitx.Repo
	Ref                string
	Constitution       constitution.Constitution
	ConstitutionDigest string
}

func (g Guard) Merge(ctx context.Context, commit string, env attest.Envelope) (Result, error) {
	defer g.Repo.DeleteRef(context.WithoutCancel(ctx), CandidatePrefix+commit)
	reject := func(reason Reason, detail string) (Result, error) {
		return Result{Status: Rejected, Reason: reason, Detail: detail, Commit: commit}, nil
	}

	trusted, err := g.Constitution.TrustedKeys()
	if err != nil {
		return Result{}, err
	}
	statement, _, err := attest.VerifyStatement(env, trusted)
	if err != nil {
		return reject(ReasonUntrusted, err.Error())
	}
	attestedTree, err := statement.Tree()
	if err != nil {
		return reject(ReasonUntrusted, err.Error())
	}
	tree, err := g.Repo.Tree(ctx, commit)
	if err != nil {
		return Result{}, fmt.Errorf("resolve candidate %s: %w", commit, err)
	}
	if tree != attestedTree {
		return reject(ReasonTreeMismatch, fmt.Sprintf("commit tree %s, attested tree %s", tree, attestedTree))
	}

	predicate := statement.Predicate
	if predicate.Constitution != g.ConstitutionDigest {
		return reject(ReasonConstitution, fmt.Sprintf("attested under %s, line is at %s", predicate.Constitution, g.ConstitutionDigest))
	}
	if want := g.Constitution.Runner.ID(); predicate.Runner != want {
		return reject(ReasonRunner, fmt.Sprintf("checks ran on %s, constitution requires %s", predicate.Runner, want))
	}
	if reason, detail := checkResults(g.Constitution.Checks.Required, predicate.Checks); reason != "" {
		return reject(reason, detail)
	}

	stopped, err := g.Repo.RefExists(ctx, StopRef)
	if err != nil {
		return Result{}, err
	}
	if stopped {
		return reject(ReasonLineStopped, "the andon cord is pulled")
	}

	base, err := g.Repo.Ref(ctx, g.Ref)
	if err != nil {
		return Result{}, fmt.Errorf("read %s: %w", g.Ref, err)
	}
	parents, err := g.Repo.Parents(ctx, commit)
	if err != nil {
		return Result{}, err
	}
	if len(parents) == 0 || parents[0] != base {
		result, _ := reject(ReasonBaseMoved, "the candidate's first parent is not the current "+g.Ref+"; rebase and retry")
		result.Base = base
		return result, nil
	}

	paths, err := g.Repo.ChangedPaths(ctx, base, commit)
	if err != nil {
		return Result{}, err
	}
	for _, p := range paths {
		if roles, guarded := g.Constitution.AllowedRoles(p); guarded && !slices.Contains(roles, predicate.Role) {
			return reject(ReasonRoleForbidden, fmt.Sprintf("%s may only be changed by %v, not %s", p, roles, predicate.Role))
		}
	}

	if err := g.Repo.UpdateRef(ctx, g.Ref, commit, base); err != nil {
		if errors.Is(err, gitx.ErrRefMoved) {
			result, _ := reject(ReasonBaseMoved, g.Ref+" moved during the merge; rebase and retry")
			result.Base = base
			return result, nil
		}
		return Result{}, err
	}
	return Result{Status: Merged, Commit: commit, Base: base}, nil
}

func checkResults(required []constitution.Check, results []attest.CheckResult) (Reason, string) {
	byName := make(map[string]attest.CheckResult, len(results))
	for _, r := range results {
		byName[r.Name] = r
	}
	for _, check := range required {
		r, ok := byName[check.Name]
		if !ok || r.Command != check.Command {
			return ReasonChecksMissing, fmt.Sprintf("required check %q (%s) is not attested", check.Name, check.Command)
		}
		if r.ExitCode != 0 {
			return ReasonChecksFailed, fmt.Sprintf("check %q exited %d", check.Name, r.ExitCode)
		}
	}
	return "", ""
}
```

- [ ] **Step 4: Implement the hook installer**

`line/guard/hooks.go`:
```go
package guard

import (
	"fmt"
	"os"
	"path/filepath"
)

const preReceive = `#!/bin/sh
# Installed by sweatshop. Only witness candidates may be pushed; branches move
# exclusively through the merge guard, which updates refs directly.
while read old new ref; do
	case "$ref" in
	refs/line/candidates/*) ;;
	*) echo "sweatshop: push to $ref rejected; submit through the witness" >&2; exit 1 ;;
	esac
done
`

// InstallHooks installs the pre-receive hook into a bare repository. It is
// idempotent and refuses to replace a hook it did not write.
func InstallHooks(repoDir string) error {
	path := filepath.Join(repoDir, "hooks", "pre-receive")
	existing, err := os.ReadFile(path)
	if err == nil {
		if string(existing) == preReceive {
			return nil
		}
		return fmt.Errorf("%s already exists with other content; refusing to overwrite", path)
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(preReceive), 0o755)
}
```

- [ ] **Step 5: Run tests**

Run: `cd line && go test ./guard/ && go vet ./... && test -z "$(gofmt -l .)"`
Expected: PASS (all 12 guard + hook tests).

- [ ] **Step 6: Commit**

```bash
git add line/guard
git commit -m "feat(line): merge guard — attestation-gated fast-forward CAS and push hook"
```

---

### Task 5: `witness` package — clean-room attestation and merge

**Files:**
- Create: `line/witness/runner.go`, `line/witness/witness.go`, `line/witness/merge.go`
- Test: `line/witness/runner_test.go`, `line/witness/witness_test.go`, `line/witness/merge_test.go`

**Interfaces:**
- Consumes: `attest.*` (Task 1); `constitution.Constitution`, `constitution.Runner`, `(Runner).ID` (Task 2); `gitx.Repo` (Task 3); `guard.Result`, `guard.Rejected`, `guard.ReasonChecksFailed`, `guard.CandidatePrefix` (Task 4).
- Produces:
  - `type RunResult struct{ ExitCode int; EvidenceID string }`
  - `type Runner interface{ Run(ctx context.Context, dir, command string) (RunResult, error) }`
  - `type ExecRunner struct{}`; `type ContainerRunner struct{ Inner Runner; Engine, Image string }`
  - `func NewRunner(spec constitution.Runner, host Runner) Runner`
  - `type Witness struct{ Key ed25519.PrivateKey; Role, AgentID string; Constitution constitution.Constitution; ConstitutionDigest string; Runner Runner; CacheDir string; Now func() time.Time }` — always used by pointer (holds a mutex)
  - `type AttestResult struct{ Commit string; Passed bool; Statement attest.Statement; Envelope *attest.Envelope }` (JSON `commit`, `passed`, `statement`, `envelope`)
  - `func (*Witness) Attest(ctx context.Context, sourceDir, rev string) (AttestResult, error)`
  - `type Merger interface{ Merge(ctx context.Context, commit string, env attest.Envelope) (guard.Result, error) }` — `guard.Guard` satisfies it
  - `type MergeResult struct{ Attestation AttestResult; Guard guard.Result }` (JSON `attestation`, `guard`)
  - `func (*Witness) Merge(ctx context.Context, sourceDir, rev, remote string, merger Merger) (MergeResult, error)`

- [ ] **Step 1: Write failing runner tests**

`line/witness/runner_test.go`:
```go
package witness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/constitution"
)

type recordingRunner struct {
	dir, command string
}

func (r *recordingRunner) Run(_ context.Context, dir, command string) (RunResult, error) {
	r.dir, r.command = dir, command
	return RunResult{ExitCode: 0, EvidenceID: "inv_rec"}, nil
}

func TestExecRunnerReportsExitCode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	passed, err := ExecRunner{}.Run(context.Background(), dir, "test -f marker")
	if err != nil || passed.ExitCode != 0 {
		t.Fatalf("run in dir = %+v, %v", passed, err)
	}
	failed, err := ExecRunner{}.Run(context.Background(), dir, "exit 3")
	if err != nil || failed.ExitCode != 3 {
		t.Fatalf("failing run = %+v, %v", failed, err)
	}
}

func TestContainerRunnerWrapsCommandThroughInner(t *testing.T) {
	inner := &recordingRunner{}
	runner := NewRunner(constitution.Runner{Kind: "container", Engine: "docker", Image: "ci:1"}, inner)
	result, err := runner.Run(context.Background(), "/tmp/check out", "echo 'hi' && just ci")
	if err != nil || result.EvidenceID != "inv_rec" {
		t.Fatalf("result = %+v, %v", result, err)
	}
	want := `docker run --rm -v '/tmp/check out':/src -w /src -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=/src 'ci:1' bash -c 'echo '\''hi'\'' && just ci'`
	if inner.command != want {
		t.Fatalf("wrapped command:\n got %s\nwant %s", inner.command, want)
	}
	if inner.dir != "/tmp/check out" {
		t.Fatalf("inner dir = %q", inner.dir)
	}
}

func TestNewRunnerLocalIsHostRunner(t *testing.T) {
	inner := &recordingRunner{}
	runner := NewRunner(constitution.Runner{Kind: "local"}, inner)
	if _, err := runner.Run(context.Background(), "/x", "true"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(inner.command, "docker") || inner.command != "true" {
		t.Fatalf("local runner wrapped the command: %q", inner.command)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd line && go test ./witness/`
Expected: FAIL — `undefined: RunResult`, `undefined: ExecRunner`, etc.

- [ ] **Step 3: Implement runners**

`line/witness/runner.go`:
```go
package witness

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/Fewbytes/sweatshop/line/constitution"
)

type RunResult struct {
	ExitCode   int
	EvidenceID string
}

// Runner executes one check command with dir as its working directory. A
// non-zero exit is a result, not an error; errors mean the check could not run.
type Runner interface {
	Run(ctx context.Context, dir, command string) (RunResult, error)
}

// ExecRunner runs checks directly on the host. Valid only at trust level none.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, dir, command string) (RunResult, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = dir
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return RunResult{ExitCode: exitErr.ExitCode()}, nil
	}
	if err != nil {
		return RunResult{}, err
	}
	return RunResult{}, nil
}

// ContainerRunner runs each check in a throwaway container from the
// constitution's image. It goes through Inner so the host runner (agentsh's
// executor) still records the run as evidence.
type ContainerRunner struct {
	Inner  Runner
	Engine string
	Image  string
}

func (c ContainerRunner) Run(ctx context.Context, dir, command string) (RunResult, error) {
	wrapped := fmt.Sprintf("%s run --rm -v %s:/src -w /src -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=/src %s bash -c %s",
		c.Engine, shellQuote(dir), shellQuote(c.Image), shellQuote(command))
	return c.Inner.Run(ctx, dir, wrapped)
}

func NewRunner(spec constitution.Runner, host Runner) Runner {
	if spec.Kind == "container" {
		return ContainerRunner{Inner: host, Engine: spec.Engine, Image: spec.Image}
	}
	return host
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
```

`GIT_CONFIG_COUNT`/`KEY`/`VALUE` count as command-line config, which git honors for `safe.directory`; without it, git inside the container refuses the host-owned checkout.

- [ ] **Step 4: Run runner tests**

Run: `cd line && go test ./witness/ -run 'Runner'`
Expected: PASS.

- [ ] **Step 5: Write failing witness tests**

`line/witness/witness_test.go`:
```go
package witness

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
)

type source struct {
	t    *testing.T
	ctx  context.Context
	repo gitx.Repo
}

// ok checks a git call: ok(t)(repo.Git(...)). Go only lets a multi-value call
// fill an argument list on its own, hence the curried form.
func ok(t *testing.T) func(string, error) {
	return func(_ string, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func newSource(t *testing.T) *source {
	t.Helper()
	s := &source{t: t, ctx: context.Background(), repo: gitx.Repo{Dir: t.TempDir()}}
	ok(t)(s.repo.Git(s.ctx, "init", "--quiet", "--initial-branch=master"))
	ok(t)(s.repo.Git(s.ctx, "config", "user.name", "test"))
	ok(t)(s.repo.Git(s.ctx, "config", "user.email", "test@example.com"))
	s.commit("check.sh", "test ! -e BROKEN\n")
	return s
}

func (s *source) write(name, content string) {
	s.t.Helper()
	full := filepath.Join(s.repo.Dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *source) commit(name, content string) string {
	s.t.Helper()
	s.write(name, content)
	ok(s.t)(s.repo.Git(s.ctx, "add", "-A"))
	ok(s.t)(s.repo.Git(s.ctx, "commit", "--quiet", "-m", "change "+name))
	sha, err := s.repo.Commit(s.ctx, "HEAD")
	if err != nil {
		s.t.Fatal(err)
	}
	return sha
}

func newWitness(t *testing.T) (*Witness, ed25519.PublicKey) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &Witness{
		Key: key, Role: "maker", AgentID: "maker-1", ConstitutionDigest: "sha256:test",
		Constitution: constitution.Constitution{
			Trust:  constitution.Trust{Level: "none"},
			Checks: constitution.Checks{Required: []constitution.Check{{Name: "check", Command: "sh check.sh"}}},
			Runner: constitution.Runner{Kind: "local"},
		},
		Runner:   ExecRunner{},
		CacheDir: filepath.Join(t.TempDir(), "cache.git"),
	}, pub
}

func TestAttestSignsPassingCommit(t *testing.T) {
	s := newSource(t)
	w, pub := newWitness(t)
	head := s.commit("src/a.go", "package a")
	result, err := w.Attest(s.ctx, s.repo.Dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed || result.Commit != head || result.Envelope == nil {
		t.Fatalf("result = %+v", result)
	}
	statement, _, err := attest.VerifyStatement(*result.Envelope, map[string]ed25519.PublicKey{attest.KeyID(pub): pub})
	if err != nil {
		t.Fatal(err)
	}
	tree, _ := statement.Tree()
	want, _ := s.repo.Tree(s.ctx, head)
	if tree != want {
		t.Fatalf("attested tree %s, commit tree %s", tree, want)
	}
	p := statement.Predicate
	if p.Role != "maker" || p.AgentID != "maker-1" || p.Constitution != "sha256:test" || p.Runner != "local" || p.Commit != head {
		t.Fatalf("predicate = %+v", p)
	}
	if len(p.Checks) != 1 || p.Checks[0].ExitCode != 0 || p.Checks[0].Command != "sh check.sh" {
		t.Fatalf("checks = %+v", p.Checks)
	}
}

func TestAttestFailingCheckIsNotSigned(t *testing.T) {
	s := newSource(t)
	w, _ := newWitness(t)
	s.commit("BROKEN", "x")
	result, err := w.Attest(s.ctx, s.repo.Dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed || result.Envelope != nil || result.Statement.Predicate.Checks[0].ExitCode == 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestAttestIgnoresUncommittedWork(t *testing.T) {
	s := newSource(t)
	w, _ := newWitness(t)
	s.write("BROKEN", "untracked file that would fail the check in the agent's tree")
	result, err := w.Attest(s.ctx, s.repo.Dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed {
		t.Fatal("check saw the agent's working tree instead of a clean checkout")
	}
}

func TestAttestNeverRunsAgentHooksOrConfig(t *testing.T) {
	s := newSource(t)
	w, _ := newWitness(t)
	marker := filepath.Join(t.TempDir(), "pwned")
	hook := "#!/bin/sh\ntouch " + marker + "\n"
	for _, name := range []string{"post-checkout", "pre-push", "post-merge", "reference-transaction"} {
		if err := os.WriteFile(filepath.Join(s.repo.Dir, ".git", "hooks", name), []byte(hook), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ok(t)(s.repo.Git(s.ctx, "config", "core.fsmonitor", "touch "+marker))
	os.Remove(marker)
	if _, err := w.Attest(s.ctx, s.repo.Dir, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("witness executed code from the agent's repository")
	}
}

func TestAttestResolvesBranchesAndRejectsUnknownRevisions(t *testing.T) {
	s := newSource(t)
	w, _ := newWitness(t)
	head := s.commit("src/a.go", "package a")
	ok(t)(s.repo.Git(s.ctx, "branch", "feature"))
	s.commit("src/b.go", "package b")
	result, err := w.Attest(s.ctx, s.repo.Dir, "feature")
	if err != nil || result.Commit != head {
		t.Fatalf("Attest(feature) = %+v, %v", result, err)
	}
	if _, err := w.Attest(s.ctx, s.repo.Dir, "no-such-branch"); err == nil {
		t.Fatal("attested a revision that does not exist")
	}
	if _, err := w.Attest(s.ctx, s.repo.Dir, "--upload-pack=touch /tmp/x"); err == nil {
		t.Fatal("accepted an option-shaped revision")
	}
}
```

- [ ] **Step 6: Run to verify failure**

Run: `cd line && go test ./witness/`
Expected: FAIL — `undefined: Witness`.

- [ ] **Step 7: Implement Attest**

`line/witness/witness.go`:
```go
// Package witness attests commits: it runs the constitution's required checks
// on a clean checkout it owns and signs the result. Agents never touch the
// checkout, the key, or the cache.
package witness

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
)

type Witness struct {
	Key                ed25519.PrivateKey
	Role               string
	AgentID            string
	Constitution       constitution.Constitution
	ConstitutionDigest string
	Runner             Runner
	// CacheDir is a witness-owned bare repository. The agent's repository is
	// only ever read through git fetch; git never runs inside it, so its
	// hooks and config cannot execute.
	CacheDir string
	Now      func() time.Time

	mu sync.Mutex
}

type AttestResult struct {
	Commit    string           `json:"commit"`
	Passed    bool             `json:"passed"`
	Statement attest.Statement `json:"statement"`
	Envelope  *attest.Envelope `json:"envelope,omitempty"`
}

func (w *Witness) Attest(ctx context.Context, sourceDir, rev string) (AttestResult, error) {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return AttestResult{}, fmt.Errorf("invalid revision %q", rev)
	}
	commit, tree, checkout, cleanup, err := w.prepare(ctx, sourceDir, rev)
	if err != nil {
		return AttestResult{}, err
	}
	defer cleanup()

	predicate := attest.ChecksPredicate{
		Commit: commit, Role: w.Role, AgentID: w.AgentID,
		Constitution: w.ConstitutionDigest, Runner: w.Constitution.Runner.ID(),
	}
	passed := true
	for _, check := range w.Constitution.Checks.Required {
		run, err := w.Runner.Run(ctx, checkout, check.Command)
		if err != nil {
			return AttestResult{}, fmt.Errorf("run check %s: %w", check.Name, err)
		}
		predicate.Checks = append(predicate.Checks, attest.CheckResult{
			Name: check.Name, Command: check.Command, ExitCode: run.ExitCode, EvidenceID: run.EvidenceID,
		})
		if run.ExitCode != 0 {
			passed = false
			break
		}
	}
	predicate.FinishedAt = w.now().UTC()

	statement := attest.NewStatement(tree, predicate)
	result := AttestResult{Commit: commit, Passed: passed, Statement: statement}
	if passed {
		env, err := attest.SignStatement(w.Key, statement)
		if err != nil {
			return AttestResult{}, err
		}
		result.Envelope = &env
	}
	return result, nil
}

// prepare serializes access to the shared cache: fetch, resolve, and check
// out under the lock; checks themselves run outside it.
func (w *Witness) prepare(ctx context.Context, sourceDir, rev string) (commit, tree, checkout string, cleanup func(), err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	cache, err := w.cache(ctx)
	if err != nil {
		return "", "", "", nil, err
	}
	if commit, err = w.fetch(ctx, cache, sourceDir, rev); err != nil {
		return "", "", "", nil, err
	}
	if tree, err = cache.Tree(ctx, commit); err != nil {
		return "", "", "", nil, err
	}
	if checkout, cleanup, err = w.checkout(ctx, cache, commit); err != nil {
		return "", "", "", nil, err
	}
	return commit, tree, checkout, cleanup, nil
}

func (w *Witness) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Witness) cache(ctx context.Context) (gitx.Repo, error) {
	repo := gitx.Repo{Dir: w.CacheDir}
	if _, err := os.Stat(filepath.Join(w.CacheDir, "HEAD")); err == nil {
		return repo, nil
	}
	if err := os.MkdirAll(w.CacheDir, 0o700); err != nil {
		return repo, err
	}
	_, err := repo.Git(ctx, "init", "--quiet", "--bare")
	return repo, err
}

func (w *Witness) fetch(ctx context.Context, cache gitx.Repo, sourceDir, rev string) (string, error) {
	_, err := cache.Git(ctx, "-c", "safe.directory="+sourceDir, "fetch", "--quiet", "--no-tags", "--force", "--end-of-options",
		sourceDir, "+HEAD:refs/source/HEAD", "+refs/heads/*:refs/source/heads/*")
	if err != nil {
		return "", fmt.Errorf("fetch from %s: %w", sourceDir, err)
	}
	for _, candidate := range []string{"refs/source/" + rev, "refs/source/heads/" + rev, rev} {
		if commit, err := cache.Commit(ctx, candidate); err == nil {
			return commit, nil
		}
	}
	return "", errors.New(rev + " is not a commit reachable from the agent's branches")
}

// checkout produces a self-contained repository at commit, so it also works
// when mounted into a container where the cache path does not exist.
func (w *Witness) checkout(ctx context.Context, cache gitx.Repo, commit string) (string, func(), error) {
	ref := "refs/witness/" + commit
	if _, err := cache.Git(ctx, "update-ref", ref, commit); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "witness-checkout-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() {
		_ = os.RemoveAll(dir)
		_ = cache.DeleteRef(context.WithoutCancel(ctx), ref)
	}
	work := gitx.Repo{Dir: dir}
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"fetch", "--quiet", "--no-tags", cache.Dir, ref},
		{"checkout", "--quiet", "--detach", "FETCH_HEAD"},
	} {
		if _, err := work.Git(ctx, args...); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return dir, cleanup, nil
}
```

- [ ] **Step 8: Run witness tests**

Run: `cd line && go test ./witness/`
Expected: PASS.

- [ ] **Step 9: Write failing merge tests**

`line/witness/merge_test.go`:
```go
package witness

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
)

type fakeMerger struct {
	remote gitx.Repo
	called bool
	commit string
	env    attest.Envelope
}

func (f *fakeMerger) Merge(ctx context.Context, commit string, env attest.Envelope) (guard.Result, error) {
	f.called, f.commit, f.env = true, commit, env
	if ok, _ := f.remote.RefExists(ctx, guard.CandidatePrefix+commit); !ok {
		return guard.Result{Status: guard.Rejected, Reason: "no_candidate", Commit: commit}, nil
	}
	return guard.Result{Status: guard.Merged, Commit: commit}, nil
}

func newRemote(t *testing.T) gitx.Repo {
	t.Helper()
	remote := gitx.Repo{Dir: filepath.Join(t.TempDir(), "remote.git")}
	ok(t)((gitx.Repo{Dir: filepath.Dir(remote.Dir)}).Git(context.Background(), "init", "--quiet", "--bare", remote.Dir))
	return remote
}

func TestMergePushesCandidateAndCallsGuard(t *testing.T) {
	s := newSource(t)
	w, _ := newWitness(t)
	remote := newRemote(t)
	merger := &fakeMerger{remote: remote}
	head := s.commit("src/a.go", "package a")
	result, err := w.Merge(s.ctx, s.repo.Dir, "HEAD", remote.Dir, merger)
	if err != nil {
		t.Fatal(err)
	}
	if !merger.called || merger.commit != head || result.Guard.Status != guard.Merged {
		t.Fatalf("result = %+v, merger = %+v", result, merger)
	}
	if len(merger.env.Signatures) != 1 {
		t.Fatal("guard did not receive the signed envelope")
	}
}

func TestMergeWithFailingChecksNeverReachesGuard(t *testing.T) {
	s := newSource(t)
	w, _ := newWitness(t)
	remote := newRemote(t)
	merger := &fakeMerger{remote: remote}
	head := s.commit("BROKEN", "x")
	result, err := w.Merge(s.ctx, s.repo.Dir, "HEAD", remote.Dir, merger)
	if err != nil {
		t.Fatal(err)
	}
	if merger.called || result.Guard.Status != guard.Rejected || result.Guard.Reason != guard.ReasonChecksFailed {
		t.Fatalf("result = %+v, merger called = %v", result, merger.called)
	}
	if ok, _ := remote.RefExists(s.ctx, guard.CandidatePrefix+head); ok {
		t.Fatal("failed commit was pushed as a candidate")
	}
}
```

- [ ] **Step 10: Run to verify failure**

Run: `cd line && go test ./witness/ -run Merge`
Expected: FAIL — `w.Merge undefined`.

- [ ] **Step 11: Implement Merge**

`line/witness/merge.go`:
```go
package witness

import (
	"context"
	"fmt"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
)

// Merger is the merge guard as the witness sees it.
type Merger interface {
	Merge(ctx context.Context, commit string, env attest.Envelope) (guard.Result, error)
}

type MergeResult struct {
	Attestation AttestResult `json:"attestation"`
	Guard       guard.Result `json:"guard"`
}

// Merge attests rev and, only if every check passed, pushes it to remote as a
// candidate and asks the guard to merge it.
func (w *Witness) Merge(ctx context.Context, sourceDir, rev, remote string, merger Merger) (MergeResult, error) {
	attestation, err := w.Attest(ctx, sourceDir, rev)
	if err != nil {
		return MergeResult{}, err
	}
	result := MergeResult{Attestation: attestation}
	if !attestation.Passed {
		result.Guard = guard.Result{
			Status: guard.Rejected, Reason: guard.ReasonChecksFailed, Commit: attestation.Commit,
			Detail: "witness checks failed; nothing was submitted",
		}
		return result, nil
	}
	cache := gitx.Repo{Dir: w.CacheDir}
	if _, err := cache.Git(ctx, "push", "--quiet", remote, attestation.Commit+":"+guard.CandidatePrefix+attestation.Commit); err != nil {
		return result, fmt.Errorf("push candidate: %w", err)
	}
	result.Guard, err = merger.Merge(ctx, attestation.Commit, *attestation.Envelope)
	return result, err
}
```

- [ ] **Step 12: Run all line tests**

Run: `cd line && go test ./... && go vet ./... && test -z "$(gofmt -l .)"`
Expected: PASS.

- [ ] **Step 13: Commit**

```bash
git add line/witness
git commit -m "feat(line): witness — clean-room checks, signed attestation, candidate submit"
```

---

### Task 6: Scenario harness — scripted fake agents end to end

**Files:**
- Create: `line/harness/harness_test.go`

**Interfaces:**
- Consumes: `attest.EncodePublicKey`; `constitution.Load`; `gitx.Repo`; `guard.Guard`, `guard.InstallHooks`, `guard.StopRef`, `guard.CandidatePrefix`, `guard.Merged`, `guard.Rejected`, `guard.Reason*`; `witness.Witness`, `witness.ExecRunner`, `witness.MergeResult`.
- Produces: nothing for later tasks (test-only package).

- [ ] **Step 1: Write the harness and scenarios**

`line/harness/harness_test.go`:
```go
// Package harness drives the merge path end to end with scripted agents: no
// LLM, real git, real witness, real guard. Each test is one line scenario.
package harness

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/witness"
)

type line struct {
	t            *testing.T
	ctx          context.Context
	dir          string
	remote       gitx.Repo
	key          ed25519.PrivateKey
	constitution constitution.Constitution
	digest       string
	guard        guard.Guard
}

type agent struct {
	line    *line
	repo    gitx.Repo
	witness *witness.Witness
}

// ok checks a git call: ok(t)(repo.Git(...)). Go only lets a multi-value call
// fill an argument list on its own, hence the curried form.
func ok(t *testing.T) func(string, error) {
	return func(_ string, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func newLine(t *testing.T) *line {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	l := &line{t: t, ctx: ctx, dir: dir, remote: gitx.Repo{Dir: filepath.Join(dir, "remote.git")}}
	root := gitx.Repo{Dir: dir}
	ok(t)(root.Git(ctx, "init", "--quiet", "--bare", "--initial-branch=master", l.remote.Dir))

	seed := gitx.Repo{Dir: filepath.Join(dir, "seed")}
	ok(t)(root.Git(ctx, "clone", "--quiet", l.remote.Dir, seed.Dir))
	ok(t)(seed.Git(ctx, "symbolic-ref", "HEAD", "refs/heads/master"))
	ok(t)(seed.Git(ctx, "-c", "user.name=seed", "-c", "user.email=seed@example.com", "commit", "--quiet", "--allow-empty", "-m", "seed"))
	writeFile(t, seed.Dir, "check.sh", "test ! -e BROKEN\n")
	ok(t)(seed.Git(ctx, "add", "-A"))
	ok(t)(seed.Git(ctx, "-c", "user.name=seed", "-c", "user.email=seed@example.com", "commit", "--quiet", "-m", "add check"))
	ok(t)(seed.Git(ctx, "push", "--quiet", "origin", "HEAD:refs/heads/master"))
	if err := guard.InstallHooks(l.remote.Dir); err != nil {
		t.Fatal(err)
	}

	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	l.key = key
	path := filepath.Join(dir, "line.toml")
	writeFile(t, dir, "line.toml", fmt.Sprintf(`[trust]
level = "none"

[[trust.keys]]
name = "witness"
public_key = %q

[runner]
kind = "local"

[[checks.required]]
name = "check"
command = "sh check.sh"

[[guards.paths]]
pattern = "acceptance/**"
roles = ["gemba"]
`, attest.EncodePublicKey(pub)))
	l.constitution, l.digest, err = constitution.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	l.guard = guard.Guard{Repo: l.remote, Ref: "refs/heads/master", Constitution: l.constitution, ConstitutionDigest: l.digest}
	return l
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (l *line) agent(name, role string) *agent {
	return l.agentWithKey(name, role, l.key)
}

func (l *line) agentWithKey(name, role string, key ed25519.PrivateKey) *agent {
	l.t.Helper()
	repo := gitx.Repo{Dir: filepath.Join(l.dir, "agents", name)}
	ok(l.t)((gitx.Repo{Dir: l.dir}).Git(l.ctx, "clone", "--quiet", l.remote.Dir, repo.Dir))
	ok(l.t)(repo.Git(l.ctx, "config", "user.name", name))
	ok(l.t)(repo.Git(l.ctx, "config", "user.email", name+"@example.com"))
	return &agent{line: l, repo: repo, witness: &witness.Witness{
		Key: key, Role: role, AgentID: name,
		Constitution: l.constitution, ConstitutionDigest: l.digest,
		Runner: witness.ExecRunner{}, CacheDir: filepath.Join(l.dir, "witness", name, "cache.git"),
	}}
}

func (l *line) master() string {
	l.t.Helper()
	sha, err := l.remote.Ref(l.ctx, "refs/heads/master")
	if err != nil {
		l.t.Fatal(err)
	}
	return sha
}

func (a *agent) change(name, content string) string {
	a.line.t.Helper()
	writeFile(a.line.t, a.repo.Dir, name, content)
	ok(a.line.t)(a.repo.Git(a.line.ctx, "add", "-A"))
	ok(a.line.t)(a.repo.Git(a.line.ctx, "commit", "--quiet", "-m", "change "+name))
	sha, err := a.repo.Commit(a.line.ctx, "HEAD")
	if err != nil {
		a.line.t.Fatal(err)
	}
	return sha
}

func (a *agent) merge() witness.MergeResult {
	a.line.t.Helper()
	result, err := a.witness.Merge(a.line.ctx, a.repo.Dir, "HEAD", a.line.remote.Dir, a.line.guard)
	if err != nil {
		a.line.t.Fatal(err)
	}
	return result
}

func (a *agent) rebase() {
	a.line.t.Helper()
	ok(a.line.t)(a.repo.Git(a.line.ctx, "fetch", "--quiet", "origin"))
	ok(a.line.t)(a.repo.Git(a.line.ctx, "rebase", "--quiet", "origin/master"))
}

func expect(t *testing.T, result witness.MergeResult, status guard.Status, reason guard.Reason) {
	t.Helper()
	if result.Guard.Status != status || result.Guard.Reason != reason {
		t.Fatalf("guard result = %+v, want %s/%s", result.Guard, status, reason)
	}
}

func TestMakerChangeMerges(t *testing.T) {
	l := newLine(t)
	maker := l.agent("maker-1", "maker")
	commit := maker.change("src/feature.go", "package src")
	expect(t, maker.merge(), guard.Merged, "")
	if l.master() != commit {
		t.Fatalf("master = %s, want %s", l.master(), commit)
	}
}

func TestFailingCheckNeverReachesTheGuard(t *testing.T) {
	l := newLine(t)
	before := l.master()
	maker := l.agent("maker-1", "maker")
	commit := maker.change("BROKEN", "this breaks the check")
	expect(t, maker.merge(), guard.Rejected, guard.ReasonChecksFailed)
	if l.master() != before {
		t.Fatal("master moved")
	}
	if ok, _ := l.remote.RefExists(l.ctx, guard.CandidatePrefix+commit); ok {
		t.Fatal("failing commit reached the remote")
	}
}

func TestLostRaceRebasesAndRetries(t *testing.T) {
	l := newLine(t)
	a := l.agent("maker-a", "maker")
	b := l.agent("maker-b", "maker")
	first := a.change("src/a.go", "package src // a")
	b.change("src/b.go", "package src // b")
	expect(t, a.merge(), guard.Merged, "")

	lost := b.merge()
	expect(t, lost, guard.Rejected, guard.ReasonBaseMoved)
	if lost.Guard.Base != first {
		t.Fatalf("base reported %s, want %s", lost.Guard.Base, first)
	}

	b.rebase()
	expect(t, b.merge(), guard.Merged, "")
	parents, err := l.remote.Parents(l.ctx, l.master())
	if err != nil || len(parents) != 1 || parents[0] != first {
		t.Fatalf("master's parent = %v, want %s (linear history)", parents, first)
	}
}

func TestForgedWitnessIsRejected(t *testing.T) {
	l := newLine(t)
	before := l.master()
	_, rogueKey, _ := ed25519.GenerateKey(rand.Reader)
	rogue := l.agentWithKey("rogue", "maker", rogueKey)
	rogue.change("src/backdoor.go", "package src")
	expect(t, rogue.merge(), guard.Rejected, guard.ReasonUntrusted)
	if l.master() != before {
		t.Fatal("master moved")
	}
}

func TestAttestationReplayedOnAnotherCommitIsRejected(t *testing.T) {
	l := newLine(t)
	maker := l.agent("maker-1", "maker")
	maker.change("src/honest.go", "package src")
	attested, err := maker.witness.Attest(l.ctx, maker.repo.Dir, "HEAD")
	if err != nil || !attested.Passed {
		t.Fatalf("attest: %+v %v", attested, err)
	}
	sneaky := maker.change("src/sneaky.go", "package src // never attested")
	ok(t)(maker.repo.Git(l.ctx, "push", "--quiet", "origin", sneaky+":"+guard.CandidatePrefix+sneaky))
	result, err := l.guard.Merge(l.ctx, sneaky, *attested.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != guard.Rejected || result.Reason != guard.ReasonTreeMismatch {
		t.Fatalf("result = %+v", result)
	}
}

func TestDirectPushToMasterIsRejected(t *testing.T) {
	l := newLine(t)
	maker := l.agent("maker-1", "maker")
	maker.change("src/a.go", "package src")
	_, err := maker.repo.Git(l.ctx, "push", "origin", "HEAD:master")
	if err == nil || !strings.Contains(err.Error(), "submit through the witness") {
		t.Fatalf("direct push: err = %v", err)
	}
}

func TestStoppedLineRejectsUntilCleared(t *testing.T) {
	l := newLine(t)
	ok(t)(l.remote.Git(l.ctx, "update-ref", guard.StopRef, l.master()))
	maker := l.agent("maker-1", "maker")
	commit := maker.change("src/a.go", "package src")
	expect(t, maker.merge(), guard.Rejected, guard.ReasonLineStopped)

	if err := l.remote.DeleteRef(l.ctx, guard.StopRef); err != nil {
		t.Fatal(err)
	}
	expect(t, maker.merge(), guard.Merged, "")
	if l.master() != commit {
		t.Fatal("merge after clearing the stop did not land")
	}
}

func TestOnlyGembaChangesAcceptanceTests(t *testing.T) {
	l := newLine(t)
	maker := l.agent("maker-1", "maker")
	maker.change("acceptance/login_test.sh", "exit 0")
	expect(t, maker.merge(), guard.Rejected, guard.ReasonRoleForbidden)

	gemba := l.agent("gemba-1", "gemba")
	commit := gemba.change("acceptance/login_test.sh", "test -f src/login.go")
	expect(t, gemba.merge(), guard.Merged, "")
	if l.master() != commit {
		t.Fatal("gemba change did not land")
	}
}
```

- [ ] **Step 2: Run the harness**

Run: `cd line && go test ./harness/ -v`
Expected: PASS, 8 scenarios. If any scenario fails, the defect is in Tasks 1–5, not here — fix it there and re-run that task's tests before returning.

- [ ] **Step 3: Run the whole module with the race detector**

Run: `cd line && go test -race ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add line/harness
git commit -m "test(line): end-to-end scenario harness with scripted agents"
```

---

### Task 7: `sweatshop` CLI and build wiring

**Files:**
- Create: `line/cmd/sweatshop/main.go`
- Test: `line/cmd/sweatshop/main_test.go`
- Modify: `justfile` (variables, `build`, `test`, `lint`, `install`)
- Modify: `README.md` (Core Components + Directory Structure)

**Interfaces:**
- Consumes: `attest.GenerateKeyFile`, `attest.KeyID`, `attest.EncodePublicKey`; `guard.InstallHooks`.
- Produces: binary `bin/sweatshop` with `sweatshop keygen PATH` (prints key ID and public key) and `sweatshop guard install-hooks REPO`; exported-for-test `func run(args []string, stdout io.Writer) error`.

- [ ] **Step 1: Write failing CLI tests**

`line/cmd/sweatshop/main_test.go`:
```go
package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/gitx"
)

func TestKeygenPrintsKeyIDAndPublicKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "witness.key")
	var out bytes.Buffer
	if err := run([]string{"keygen", path}, &out); err != nil {
		t.Fatal(err)
	}
	pub, err := os.ReadFile(path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "key_id ") || !strings.Contains(out.String(), strings.TrimSpace(string(pub))) {
		t.Fatalf("output = %q", out.String())
	}
}

func TestGuardInstallHooks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "remote.git")
	if _, err := (gitx.Repo{Dir: filepath.Dir(dir)}).Git(context.Background(), "init", "--quiet", "--bare", dir); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"guard", "install-hooks", dir}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hooks", "pre-receive")); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownCommand(t *testing.T) {
	if err := run([]string{"frobnicate"}, &bytes.Buffer{}); err == nil {
		t.Fatal("unknown command accepted")
	}
	if err := run(nil, &bytes.Buffer{}); err == nil {
		t.Fatal("empty command line accepted")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd line && go test ./cmd/sweatshop/`
Expected: FAIL — `undefined: run`.

- [ ] **Step 3: Implement**

`line/cmd/sweatshop/main.go`:
```go
// Command sweatshop administers an assembly line: witness keys and the merge
// guard's repository hook.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/guard"
)

const usage = `usage:
  sweatshop keygen PATH                 generate a witness signing key (PATH, PATH.pub)
  sweatshop guard install-hooks REPO    install the merge guard's pre-receive hook in a bare repo`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "sweatshop:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	switch {
	case len(args) == 2 && args[0] == "keygen":
		pub, err := attest.GenerateKeyFile(args[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "key_id %s\npublic_key %s\n", attest.KeyID(pub), attest.EncodePublicKey(pub))
		return nil
	case len(args) == 3 && args[0] == "guard" && args[1] == "install-hooks":
		return guard.InstallHooks(args[2])
	default:
		return errors.New(usage)
	}
}
```

- [ ] **Step 4: Run tests**

Run: `cd line && go test ./cmd/sweatshop/ && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Wire into the justfile**

In `justfile`, after `depmaint_dir := "depmaint"` add:
```
line_dir := "line"
```
In `build`, after the depmaint line add:
```
    cd {{line_dir}} && go build -o ../{{bin_dir}}/sweatshop ./cmd/sweatshop
```
In `test`, after the depmaint line add:
```
    cd {{line_dir}} && go test ./...
```
In `lint`, after the last depmaint line add:
```
    cd {{line_dir}} && go vet ./...
    cd {{line_dir}} && test -z "$(gofmt -l .)" || (echo "gofmt needs to run on:" && gofmt -l . && exit 1)
    cd {{line_dir}} && go mod tidy && git diff --exit-code -- go.mod go.sum
```
In `install`, after the depmaint line add:
```
    cd {{line_dir}} && go build -o {{dest}}/sweatshop ./cmd/sweatshop
```

- [ ] **Step 6: Document the component**

In `README.md`, under "Core Components" after the beads section add:
```markdown
### 3. `line` — Assembly Line Primitives
Witness attestations (DSSE/in-toto, Ed25519), the constitution (line rules), and the merge guard that fast-forwards the guarded branch only for trusted, passing attestations. agentsh embeds the witness; `sweatshop` administers keys and the guard's repository hook.
*See [`design-docs/assembly-line-spec.md`](design-docs/assembly-line-spec.md)*.
```
Renumber the following "Agent Skills & Extensions" heading to `### 4.`. In "Directory Structure" add the line `├── line/             # Assembly line: witness attestations, constitution, merge guard` after the `agentsh/` line.

- [ ] **Step 7: Verify the build**

Run: `just build && ./bin/sweatshop 2>&1 | head -3`
Expected: build succeeds; the second command prints the usage text (exit status 1 is expected).

- [ ] **Step 8: Commit**

```bash
git add line/cmd justfile README.md
git commit -m "feat(line): sweatshop CLI (keygen, guard install-hooks) and build wiring"
```

---

### Task 8: agentsh — witness configuration

**Files:**
- Modify: `agentsh/go.mod` (require + replace `line`)
- Modify: `agentsh/internal/config/config.go` (add `Witness` type, `Config.Witness`, validation in `Load`)
- Test: `agentsh/internal/config/witness_test.go`

**Interfaces:**
- Consumes: `constitution.Roles` (Task 2).
- Produces:
  - `type Witness struct{ KeyFile, Role, AgentID, Constitution, StateDir, Remote, Ref string }` (JSON `key_file`, `role`, `agent_id`, `constitution`, `state_dir`, `remote`, `ref`)
  - `Config.Witness *Witness` (JSON `witness`, omitempty). After `Load`, all paths are absolute with `~` expanded and `Ref` defaults to `refs/heads/master`.

- [ ] **Step 1: Add the module dependency**

In `agentsh/go.mod`, add to the first `require` block:
```
	github.com/Fewbytes/sweatshop/line v0.0.0-00010101000000-000000000000
```
and append at the end of the file:
```
replace github.com/Fewbytes/sweatshop/line => ../line
```
Run: `cd agentsh && go mod tidy`
Expected: succeeds (tidy will drop the requirement again until something imports it — that is fine; Step 4 adds the import and Step 5 re-runs tidy).

- [ ] **Step 2: Write failing tests**

`agentsh/internal/config/witness_test.go`:
```go
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

func writeConfig(t *testing.T, path, content string) string {
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
	path := writeConfig(t, filepath.Join(f.outside, "witness.json"), f.witnessJSON(nil))
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
	path := writeConfig(t, filepath.Join(f.workspace, ".agentsh", "config.json"), f.witnessJSON(nil))
	if _, err := Load("", f.workspace); err == nil {
		t.Fatalf("loaded a witness config from inside the workspace (%s)", path)
	}
}

func TestWitnessPathsInsideWorkspaceAreRejected(t *testing.T) {
	for _, field := range []string{"key_file", "constitution", "state_dir", "remote"} {
		t.Run(field, func(t *testing.T) {
			f := newWitnessFixture(t)
			doc := f.witnessJSON(map[string]string{field: filepath.Join(f.workspace, "inside")})
			path := writeConfig(t, filepath.Join(f.outside, "witness.json"), doc)
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
			path := writeConfig(t, filepath.Join(f.outside, "witness.json"), f.witnessJSON(overrides))
			if _, err := Load(path, f.workspace); err == nil {
				t.Fatal("accepted an invalid witness config")
			}
		})
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `cd agentsh && go test ./internal/config/ -run Witness`
Expected: FAIL — `cfg.Witness undefined`.

- [ ] **Step 4: Implement**

In `agentsh/internal/config/config.go`:

Add imports `"slices"` and `"github.com/Fewbytes/sweatshop/line/constitution"`.

Add after the `Turso` type:
```go
// Witness binds this daemon to one agent and role for its lifetime and gives
// it a signing key. Everything here must be out of the agent's reach: a config
// the agent could edit would let it pick its own role and key.
type Witness struct {
	KeyFile      string `json:"key_file"`
	Role         string `json:"role"`
	AgentID      string `json:"agent_id"`
	Constitution string `json:"constitution"`
	StateDir     string `json:"state_dir"`
	Remote       string `json:"remote"`
	Ref          string `json:"ref,omitempty"`
}
```

Add a field to `Config` after `Turso`:
```go
	Witness *Witness `json:"witness,omitempty"`
```

In `Load`, immediately before the final `return config, nil`, add:
```go
	if err := config.validateWitness(workspaceRoot); err != nil {
		return config, err
	}
```

Add the methods:
```go
func (c *Config) validateWitness(workspaceRoot string) error {
	w := c.Witness
	if w == nil {
		return nil
	}
	fields := []struct {
		name  string
		value *string
		path  bool
	}{
		{"key_file", &w.KeyFile, true}, {"role", &w.Role, false}, {"agent_id", &w.AgentID, false},
		{"constitution", &w.Constitution, true}, {"state_dir", &w.StateDir, true}, {"remote", &w.Remote, true},
	}
	for _, f := range fields {
		if *f.value == "" {
			return fmt.Errorf("config %s: witness.%s is required", c.Path, f.name)
		}
		if !f.path {
			continue
		}
		expanded, err := expandHome(*f.value)
		if err != nil {
			return err
		}
		if *f.value, err = filepath.Abs(expanded); err != nil {
			return err
		}
	}
	if !slices.Contains(constitution.Roles, w.Role) {
		return fmt.Errorf("config %s: witness.role %q is not one of %v", c.Path, w.Role, constitution.Roles)
	}
	if w.Ref == "" {
		w.Ref = "refs/heads/master"
	}
	if workspaceRoot == "" {
		return nil
	}
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return err
	}
	configPath, err := filepath.Abs(c.Path)
	if err != nil {
		return err
	}
	for name, p := range map[string]string{
		"the config file": configPath, "witness.key_file": w.KeyFile, "witness.constitution": w.Constitution,
		"witness.state_dir": w.StateDir, "witness.remote": w.Remote,
	} {
		if within(p, root) {
			return fmt.Errorf("%s (%s) is inside the workspace %s, where the agent can change it; keep witness settings outside", name, p, root)
		}
	}
	return nil
}

func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
```

- [ ] **Step 5: Run tests and tidy**

Run: `cd agentsh && go mod tidy && go test ./internal/config/ && go vet ./internal/config/`
Expected: PASS; `go.mod` keeps the `line` require + replace; `go.sum` gains the go-toml entries.

- [ ] **Step 6: Commit**

```bash
git add agentsh/go.mod agentsh/go.sum agentsh/internal/config
git commit -m "feat(agentsh): witness config section, kept out of the agent's workspace"
```

---

### Task 9: agentsh — attestation storage

**Files:**
- Modify: `agentsh/internal/storage/store.go` (append migration 2 to `migrations`)
- Create: `agentsh/internal/storage/attestation.go`
- Test: `agentsh/internal/storage/attestation_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `type Attestation struct{ ID, Commit, Tree string; Passed bool; Statement, Envelope []byte; CreatedAt time.Time }`
  - `func (*Store) SaveAttestation(ctx context.Context, a Attestation) error`
  - `func (*Store) GetAttestation(ctx context.Context, id string) (Attestation, error)` — `sql.ErrNoRows` wrapped when missing

- [ ] **Step 1: Write failing tests**

`agentsh/internal/storage/attestation_test.go`:
```go
package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAttestationRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "history.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	passed := Attestation{ID: "att_1", Commit: "c1", Tree: "t1", Passed: true, Statement: []byte(`{"s":1}`), Envelope: []byte(`{"e":1}`), CreatedAt: created}
	failed := Attestation{ID: "att_2", Commit: "c2", Tree: "t2", Passed: false, Statement: []byte(`{"s":2}`), CreatedAt: created}
	for _, a := range []Attestation{passed, failed} {
		if err := store.SaveAttestation(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetAttestation(ctx, "att_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Commit != "c1" || !got.Passed || string(got.Envelope) != `{"e":1}` || !got.CreatedAt.Equal(created) {
		t.Fatalf("got %+v", got)
	}
	got2, err := store.GetAttestation(ctx, "att_2")
	if err != nil {
		t.Fatal(err)
	}
	if got2.Passed || got2.Envelope != nil {
		t.Fatalf("failed attestation round-tripped as %+v", got2)
	}
	if _, err := store.GetAttestation(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing attestation err = %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd agentsh && go test ./internal/storage/ -run Attestation`
Expected: FAIL — `undefined: Attestation`.

- [ ] **Step 3: Add the migration**

In `agentsh/internal/storage/store.go`, append to the `migrations` slice (after the version-1 entry's closing `},`):
```go
	{
		version: 2,
		apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS attestations (
				id TEXT PRIMARY KEY, commit_sha TEXT NOT NULL, tree TEXT NOT NULL,
				passed INTEGER NOT NULL, statement TEXT NOT NULL, envelope TEXT,
				created_at TEXT NOT NULL
			)`)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS attestations_commit ON attestations(commit_sha)`)
			return err
		},
	},
```

- [ ] **Step 4: Implement the accessors**

`agentsh/internal/storage/attestation.go`:
```go
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Attestation is the witness's record of one attest run. Failed runs are kept
// too: they are evidence for the agent's track record.
type Attestation struct {
	ID        string
	Commit    string
	Tree      string
	Passed    bool
	Statement []byte
	Envelope  []byte
	CreatedAt time.Time
}

func (s *Store) SaveAttestation(ctx context.Context, a Attestation) error {
	var envelope any
	if a.Envelope != nil {
		envelope = string(a.Envelope)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO attestations(id, commit_sha, tree, passed, statement, envelope, created_at)
		VALUES(?,?,?,?,?,?,?)`, a.ID, a.Commit, a.Tree, a.Passed, string(a.Statement), envelope, a.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) GetAttestation(ctx context.Context, id string) (Attestation, error) {
	var a Attestation
	var statement, created string
	var envelope sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, commit_sha, tree, passed, statement, envelope, created_at
		FROM attestations WHERE id=?`, id).Scan(&a.ID, &a.Commit, &a.Tree, &a.Passed, &statement, &envelope, &created)
	if err != nil {
		return Attestation{}, fmt.Errorf("attestation %s: %w", id, err)
	}
	a.Statement = []byte(statement)
	if envelope.Valid {
		a.Envelope = []byte(envelope.String)
	}
	if a.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return Attestation{}, err
	}
	return a, nil
}
```

- [ ] **Step 5: Run storage tests**

Run: `cd agentsh && go test ./internal/storage/`
Expected: PASS, including the existing `TestSchemaVersionTableDrivesMigrations`.

- [ ] **Step 6: Commit**

```bash
git add agentsh/internal/storage
git commit -m "feat(agentsh): store witness attestations"
```

---

### Task 10: agentsh — witness RPC ops in the daemon

**Files:**
- Modify: `agentsh/internal/rpc/protocol.go` (ops + `LineRequest`)
- Create: `agentsh/internal/daemon/witness.go`
- Modify: `agentsh/internal/daemon/server.go` (fields on `Server`, setup in `Serve`, two `handlers` entries)
- Modify: `agentsh/internal/daemon/ph2_test.go` (op list)
- Test: `agentsh/internal/daemon/witness_test.go`

**Interfaces:**
- Consumes: `config.Witness` (Task 8); `storage.Attestation`, `(*Store).SaveAttestation` (Task 9); `attest.LoadPrivateKey`, `constitution.Load`, `gitx.Repo`, `guard.Guard`, `guard.InstallHooks`, `witness.Witness`, `witness.NewRunner`, `witness.RunResult`, `witness.AttestResult`, `witness.MergeResult` (line).
- Produces:
  - `const OpLineAttest = "line_attest"`, `const OpLineMerge = "line_merge"`
  - `type LineRequest struct{ Rev string }` (JSON `rev`, omitempty; empty means `HEAD`)
  - `line_attest` result: `witness.AttestResult` JSON; `line_merge` result: `witness.MergeResult` JSON; both fail with code `witness_disabled` when no witness is configured.

- [ ] **Step 1: Add protocol types**

In `agentsh/internal/rpc/protocol.go`, add to the `const` op block after `OpBashServiceLogs`:
```go

	OpLineAttest = "line_attest"
	OpLineMerge  = "line_merge"
```
and add after `BashGCRequest`:
```go
// LineRequest names the commit a witness op acts on. Empty Rev means HEAD of
// the workspace repository.
type LineRequest struct {
	Rev string `json:"rev,omitempty"`
}
```

- [ ] **Step 2: Write failing daemon tests**

In `agentsh/internal/daemon/ph2_test.go`, change the `ops` list in `TestDispatchTableCoversEveryDeclaredOp` to end with:
```go
		agentrpc.OpBashService, agentrpc.OpBashServiceStatus, agentrpc.OpBashServiceKill, agentrpc.OpBashServiceLogs,
		agentrpc.OpLineAttest, agentrpc.OpLineMerge,
	}
```

`agentsh/internal/daemon/witness_test.go`:
```go
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/agentsh/internal/config"
	agentrpc "github.com/Fewbytes/sweatshop/agentsh/internal/rpc"
	"github.com/Fewbytes/sweatshop/agentsh/internal/workspace"
	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/witness"
)

func gitMust(t *testing.T, r gitx.Repo, args ...string) {
	t.Helper()
	if _, err := r.Git(context.Background(), args...); err != nil {
		t.Fatal(err)
	}
}

// startWitnessServer builds a remote with a seeded master, a workspace cloned
// from it, a key and constitution outside the workspace, and a daemon serving
// the workspace with the witness enabled.
func startWitnessServer(t *testing.T) (agentrpc.Client, gitx.Repo, gitx.Repo) {
	t.Helper()
	outside := t.TempDir()
	remote := gitx.Repo{Dir: filepath.Join(outside, "remote.git")}
	gitMust(t, gitx.Repo{Dir: outside}, "init", "--quiet", "--bare", "--initial-branch=master", remote.Dir)

	work := gitx.Repo{Dir: filepath.Join(t.TempDir(), "workspace")}
	gitMust(t, gitx.Repo{Dir: filepath.Dir(work.Dir)}, "clone", "--quiet", remote.Dir, work.Dir)
	gitMust(t, work, "symbolic-ref", "HEAD", "refs/heads/master")
	gitMust(t, work, "config", "user.name", "maker")
	gitMust(t, work, "config", "user.email", "maker@example.com")
	if err := os.WriteFile(filepath.Join(work.Dir, "check.sh"), []byte("test ! -e BROKEN\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The daemon keeps its database and socket in .agentsh/ inside the
	// workspace; later `git add -A` calls must not pick them up.
	if err := os.WriteFile(filepath.Join(work.Dir, ".gitignore"), []byte(".agentsh/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitMust(t, work, "add", "-A")
	gitMust(t, work, "commit", "--quiet", "-m", "seed")
	gitMust(t, work, "push", "--quiet", "origin", "HEAD:refs/heads/master")

	keyFile := filepath.Join(outside, "witness.key")
	pub, err := attest.GenerateKeyFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	constitutionFile := filepath.Join(outside, "line.toml")
	doc := fmt.Sprintf("[trust]\nlevel = \"none\"\n\n[[trust.keys]]\nname = \"w\"\npublic_key = %q\n\n[runner]\nkind = \"local\"\n\n[[checks.required]]\nname = \"check\"\ncommand = \"sh check.sh\"\n", attest.EncodePublicKey(pub))
	if err := os.WriteFile(constitutionFile, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	paths, err := workspace.Resolve(work.Dir)
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithConfig(paths, config.Config{Witness: &config.Witness{
		KeyFile: keyFile, Role: "maker", AgentID: "maker-1", Constitution: constitutionFile,
		StateDir: filepath.Join(outside, "state"), Remote: remote.Dir, Ref: "refs/heads/master",
	}})
	server.Grace = 100 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	t.Cleanup(func() {
		server.Shutdown()
		<-done
	})
	waitForSocket(t, paths.Socket)
	return agentrpc.Client{Socket: paths.Socket, Timeout: 30 * time.Second}, work, remote
}

func callLine(t *testing.T, client agentrpc.Client, op string, result any) error {
	t.Helper()
	params, _ := json.Marshal(agentrpc.LineRequest{})
	return client.Call(context.Background(), agentrpc.Request{ID: op, Op: op, Params: params}, result)
}

func TestLineMergeAttestsWithEvidenceAndMerges(t *testing.T) {
	client, work, remote := startWitnessServer(t)
	if err := os.WriteFile(filepath.Join(work.Dir, "feature.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitMust(t, work, "add", "-A")
	gitMust(t, work, "commit", "--quiet", "-m", "feature")
	head, _ := work.Commit(context.Background(), "HEAD")

	var result witness.MergeResult
	if err := callLine(t, client, agentrpc.OpLineMerge, &result); err != nil {
		t.Fatal(err)
	}
	if result.Guard.Status != guard.Merged {
		t.Fatalf("guard = %+v", result.Guard)
	}
	if master, _ := remote.Ref(context.Background(), "refs/heads/master"); master != head {
		t.Fatalf("remote master = %s, want %s", master, head)
	}
	checks := result.Attestation.Statement.Predicate.Checks
	if len(checks) != 1 || checks[0].EvidenceID == "" {
		t.Fatalf("checks carry no evidence: %+v", checks)
	}
	if result.Attestation.Statement.Predicate.Role != "maker" {
		t.Fatalf("role = %q", result.Attestation.Statement.Predicate.Role)
	}
}

func TestLineAttestReportsFailureWithoutEnvelope(t *testing.T) {
	client, work, _ := startWitnessServer(t)
	if err := os.WriteFile(filepath.Join(work.Dir, "BROKEN"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitMust(t, work, "add", "-A")
	gitMust(t, work, "commit", "--quiet", "-m", "break")
	var result witness.AttestResult
	if err := callLine(t, client, agentrpc.OpLineAttest, &result); err != nil {
		t.Fatal(err)
	}
	if result.Passed || result.Envelope != nil {
		t.Fatalf("result = %+v", result)
	}
}

func TestLineOpsRequireAWitness(t *testing.T) {
	_, client := startTestServer(t)
	err := callLine(t, client, agentrpc.OpLineMerge, nil)
	if err == nil || !strings.Contains(err.Error(), "witness_disabled") {
		t.Fatalf("err = %v, want witness_disabled", err)
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `cd agentsh && go test ./internal/daemon/ -run 'Line|DispatchTable'`
Expected: FAIL — dispatch table missing entries; `handleLineMerge` undefined behavior (`unknown_operation`).

- [ ] **Step 4: Implement the daemon side**

`agentsh/internal/daemon/witness.go`:
```go
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Fewbytes/sweatshop/agentsh/internal/executor"
	agentrpc "github.com/Fewbytes/sweatshop/agentsh/internal/rpc"
	"github.com/Fewbytes/sweatshop/agentsh/internal/storage"
	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/witness"
)

const witnessCheckTimeout = 30 * time.Minute

// setupWitness binds the daemon to the configured agent and role. In v1 the
// merge guard runs in-process against a local bare repository.
func (s *Server) setupWitness() error {
	cfg := s.Config.Witness
	key, err := attest.LoadPrivateKey(cfg.KeyFile)
	if err != nil {
		return fmt.Errorf("witness key: %w", err)
	}
	rules, digest, err := constitution.Load(cfg.Constitution)
	if err != nil {
		return fmt.Errorf("witness constitution: %w", err)
	}
	if err := guard.InstallHooks(cfg.Remote); err != nil {
		return fmt.Errorf("guard hooks: %w", err)
	}
	s.witness = &witness.Witness{
		Key: key, Role: cfg.Role, AgentID: cfg.AgentID,
		Constitution: rules, ConstitutionDigest: digest,
		Runner:   witness.NewRunner(rules.Runner, executorRunner{exec: s.exec}),
		CacheDir: filepath.Join(cfg.StateDir, "cache.git"),
	}
	s.guard = guard.Guard{Repo: gitx.Repo{Dir: cfg.Remote}, Ref: cfg.Ref, Constitution: rules, ConstitutionDigest: digest}
	return nil
}

// executorRunner runs witness checks through the supervised executor, so each
// check leaves an invocation record whose ID becomes the check's evidence.
type executorRunner struct {
	exec *executor.Executor
}

func (r executorRunner) Run(ctx context.Context, dir, command string) (witness.RunResult, error) {
	invocation, err := r.exec.Execute(ctx, executor.Request{
		Command: command, Session: "witness-" + filepath.Base(dir), CWD: dir, Timeout: witnessCheckTimeout,
	})
	if err != nil {
		return witness.RunResult{}, err
	}
	code := -1
	if invocation.ExitCode != nil {
		code = *invocation.ExitCode
	}
	return witness.RunResult{ExitCode: code, EvidenceID: invocation.ID}, nil
}

func witnessDisabled(request agentrpc.Request) (agentrpc.Response, func()) {
	return agentrpc.Failure(request.ID, "witness_disabled", "this daemon has no witness configured"), nil
}

func lineRev(request agentrpc.Request) (string, error) {
	params, err := decodeOptionalParams[agentrpc.LineRequest](request.Params)
	if err != nil {
		return "", err
	}
	if params.Rev == "" {
		return "HEAD", nil
	}
	return params.Rev, nil
}

func handleLineAttest(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	if s.witness == nil {
		return witnessDisabled(request)
	}
	rev, err := lineRev(request)
	if err != nil {
		return invalidParams(request, err)
	}
	result, err := s.witness.Attest(ctx, s.paths.Root, rev)
	if err != nil {
		return agentrpc.Failure(request.ID, "attest", err.Error()), nil
	}
	if err := s.recordAttestation(ctx, result); err != nil {
		return agentrpc.Failure(request.ID, "storage", err.Error()), nil
	}
	return agentrpc.Success(request.ID, result), nil
}

func handleLineMerge(s *Server, ctx context.Context, request agentrpc.Request) (agentrpc.Response, func()) {
	if s.witness == nil {
		return witnessDisabled(request)
	}
	rev, err := lineRev(request)
	if err != nil {
		return invalidParams(request, err)
	}
	result, err := s.witness.Merge(ctx, s.paths.Root, rev, s.Config.Witness.Remote, s.guard)
	if result.Attestation.Commit != "" {
		if recordErr := s.recordAttestation(ctx, result.Attestation); recordErr != nil && err == nil {
			err = recordErr
		}
	}
	if err != nil {
		return agentrpc.Failure(request.ID, "merge", err.Error()), nil
	}
	return agentrpc.Success(request.ID, result), nil
}

func (s *Server) recordAttestation(ctx context.Context, result witness.AttestResult) error {
	statement, err := json.Marshal(result.Statement)
	if err != nil {
		return err
	}
	var envelope []byte
	if result.Envelope != nil {
		if envelope, err = json.Marshal(result.Envelope); err != nil {
			return err
		}
	}
	tree, _ := result.Statement.Tree()
	return s.store.SaveAttestation(ctx, storage.Attestation{
		ID: newAttestationID(), Commit: result.Commit, Tree: tree, Passed: result.Passed,
		Statement: statement, Envelope: envelope, CreatedAt: time.Now().UTC(),
	})
}

func newAttestationID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "att_" + hex.EncodeToString(b[:])
}
```

In `agentsh/internal/daemon/server.go`:

Add imports `"github.com/Fewbytes/sweatshop/line/guard"` and `"github.com/Fewbytes/sweatshop/line/witness"`.

Add fields to `Server` after `blobs storage.BlobStore`:
```go
	// witness and guard are set only when Config.Witness is present; the
	// line_* ops report witness_disabled otherwise.
	witness *witness.Witness
	guard   guard.Guard
```

In `Serve`, immediately after the `if s.Grace > 0 { ... }` block, add:
```go
	if s.Config.Witness != nil {
		if err := s.setupWitness(); err != nil {
			_ = ln.Close()
			store.Close()
			return err
		}
	}
```

Add to the `handlers` map:
```go
	agentrpc.OpLineAttest:        handleLineAttest,
	agentrpc.OpLineMerge:         handleLineMerge,
```

- [ ] **Step 5: Run daemon tests**

Run: `cd agentsh && go test ./internal/daemon/ && go vet ./...`
Expected: PASS (all existing daemon tests plus the three new ones).

- [ ] **Step 6: Commit**

```bash
git add agentsh/internal/rpc agentsh/internal/daemon
git commit -m "feat(agentsh): line_attest and line_merge ops — agentshd as witness"
```

---

### Task 11: agentsh CLI — `attest` and `merge`

**Files:**
- Modify: `agentsh/cmd/agentsh/main.go` (two `commands` entries, two builders, longer client timeout for them)
- Test: `agentsh/cmd/agentsh/line_test.go`

**Interfaces:**
- Consumes: `agentrpc.OpLineAttest`, `agentrpc.OpLineMerge`, `agentrpc.LineRequest` (Task 10).
- Produces: `agentsh attest [--rev REV]`, `agentsh merge [--rev REV]`; `func buildAttest(args []string) (agentrpc.Request, error)`, `func buildMerge(args []string) (agentrpc.Request, error)`.

- [ ] **Step 1: Write failing tests**

`agentsh/cmd/agentsh/line_test.go`:
```go
package main

import (
	"encoding/json"
	"testing"

	agentrpc "github.com/Fewbytes/sweatshop/agentsh/internal/rpc"
)

func TestBuildMergeAndAttest(t *testing.T) {
	for _, c := range []struct {
		build func([]string) (agentrpc.Request, error)
		op    string
	}{{buildMerge, agentrpc.OpLineMerge}, {buildAttest, agentrpc.OpLineAttest}} {
		request, err := c.build([]string{"--rev", "feature"})
		if err != nil {
			t.Fatal(err)
		}
		var params agentrpc.LineRequest
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatal(err)
		}
		if request.Op != c.op || params.Rev != "feature" {
			t.Fatalf("request = %+v params = %+v", request, params)
		}
		defaulted, err := c.build(nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(defaulted.Params, &params); err != nil || params.Rev != "" {
			t.Fatalf("default rev = %q, %v", params.Rev, err)
		}
	}
}

func TestBuildMergeRejectsPositionalArgs(t *testing.T) {
	if _, err := buildMerge([]string{"extra"}); err == nil {
		t.Fatal("accepted a positional argument")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd agentsh && go test ./cmd/agentsh/ -run 'BuildMerge'`
Expected: FAIL — `undefined: buildMerge`.

- [ ] **Step 3: Implement**

In `agentsh/cmd/agentsh/main.go`, add to `commands`:
```go
	"attest":         {"agentsh attest [flags]", buildAttest},
	"merge":          {"agentsh merge [flags]", buildMerge},
```

Add the builders (next to `buildBash`):
```go
func buildAttest(args []string) (agentrpc.Request, error) {
	return buildLine("attest", agentrpc.OpLineAttest, args)
}

func buildMerge(args []string) (agentrpc.Request, error) {
	return buildLine("merge", agentrpc.OpLineMerge, args)
}

func buildLine(name, op string, args []string) (agentrpc.Request, error) {
	fs := newFlagSet(name)
	rev := fs.String("rev", "", "revision to attest (default HEAD)")
	if err := fs.Parse(args); err != nil {
		return agentrpc.Request{}, err
	}
	if fs.NArg() != 0 {
		return agentrpc.Request{}, usageErr(fs, "takes no positional arguments")
	}
	params, _ := json.Marshal(agentrpc.LineRequest{Rev: *rev})
	return agentrpc.Request{Op: op, Params: params}, nil
}
```

In `main`, replace:
```go
	client := agentrpc.Client{Socket: paths.Socket, Timeout: 180 * time.Second}
```
with:
```go
	client := agentrpc.Client{Socket: paths.Socket, Timeout: 180 * time.Second}
	if request.Op == agentrpc.OpLineAttest || request.Op == agentrpc.OpLineMerge {
		// Witness checks run the full required suite; the daemon bounds each
		// check at 30 minutes.
		client.Timeout = 2 * time.Hour
	}
```

- [ ] **Step 4: Run tests and the full gate**

Run: `cd agentsh && go test ./cmd/agentsh/ && cd .. && just ci`
Expected: PASS — lint, all module tests (agentsh, bughunt, depmaint, line), build, marketplace validation.

- [ ] **Step 5: Commit**

```bash
git add agentsh/cmd/agentsh
git commit -m "feat(agentsh): attest and merge subcommands"
```

---

## Out of scope for this plan (later build-order steps)

- Witness-signed merge commits (git SSH signing) and a ref guard requiring a trusted signer.
- Guard as its own service, separate from the witness process.
- `sweatshop line init`, constitution versioning and proposals (step 2).
- Andon guard and pulling/clearing the cord through a guarded path (step 3); the guard already honors `refs/line/andon`.
- Running agents in containers with only the agentshd socket mounted (step 3); the container check runner exists now.
- MCP tool exposure of `line_attest` / `line_merge` (agents can call `agentsh merge` through Bash).
