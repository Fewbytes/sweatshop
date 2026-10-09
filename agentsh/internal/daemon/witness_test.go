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
