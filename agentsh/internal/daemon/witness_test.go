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
	"github.com/Fewbytes/sweatshop/agentsh/internal/executor"
	agentrpc "github.com/Fewbytes/sweatshop/agentsh/internal/rpc"
	"github.com/Fewbytes/sweatshop/agentsh/internal/storage"
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

func TestLineAttestSignsFailure(t *testing.T) {
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
	if result.Passed || result.Envelope == nil {
		t.Fatalf("failing run must be signed: %+v", result)
	}
}

// Each attestation must run its checks in its own checkout. A shared executor
// session would remember the first checkout's CWD and attest the wrong tree.
func TestLineAttestSequentialCommitsUseTheirOwnCheckout(t *testing.T) {
	client, work, _ := startWitnessServer(t)
	var first witness.AttestResult
	if err := callLine(t, client, agentrpc.OpLineAttest, &first); err != nil {
		t.Fatal(err)
	}
	if !first.Passed {
		t.Fatalf("first attestation should pass: %+v", first)
	}
	if err := os.WriteFile(filepath.Join(work.Dir, "BROKEN"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitMust(t, work, "add", "-A")
	gitMust(t, work, "commit", "--quiet", "-m", "break")
	var second witness.AttestResult
	if err := callLine(t, client, agentrpc.OpLineAttest, &second); err != nil {
		t.Fatal(err)
	}
	if second.Passed {
		t.Fatalf("second attestation should fail: %+v", second)
	}
	// A failure alone would also result from a stale CWD (deleted checkout), so
	// the discriminating step is a passing attestation after a failing one.
	gitMust(t, work, "rm", "--quiet", "BROKEN")
	gitMust(t, work, "commit", "--quiet", "-m", "fix")
	var third witness.AttestResult
	if err := callLine(t, client, agentrpc.OpLineAttest, &third); err != nil {
		t.Fatal(err)
	}
	if !third.Passed {
		t.Fatalf("third attestation should pass: %+v", third)
	}
}

// Session state carries the CWD and request.CWD only seeds a new session, so
// two checkouts that are alive at once (concurrent attestations) must not
// share a session: the second would run in the first's directory.
func TestExecutorRunnerKeepsCheckoutsApart(t *testing.T) {
	root := t.TempDir()
	store, err := storage.Open(context.Background(), storage.Config{Path: filepath.Join(root, "history.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	runner := executorRunner{exec: executor.New(store, storage.BlobStore{Root: filepath.Join(root, "blobs")})}

	dirA, dirB := filepath.Join(root, "checkout-a"), filepath.Join(root, "checkout-b")
	for dir, marker := range map[string]string{dirA: "A", dirB: "B"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, marker), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, step := range []struct{ dir, command string }{{dirA, "test -e A"}, {dirB, "test -e B"}} {
		run, err := runner.Run(context.Background(), step.dir, step.command)
		if err != nil {
			t.Fatal(err)
		}
		if run.ExitCode != 0 {
			t.Fatalf("%q in %s exited %d: ran in the wrong checkout", step.command, step.dir, run.ExitCode)
		}
	}
}

// Checks of the same checkout must not share shell state either.
func TestExecutorRunnerIsolatesChecksInTheSameCheckout(t *testing.T) {
	root := t.TempDir()
	store, err := storage.Open(context.Background(), storage.Config{Path: filepath.Join(root, "history.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	runner := executorRunner{exec: executor.New(store, storage.BlobStore{Root: filepath.Join(root, "blobs")})}
	dir := filepath.Join(root, "checkout")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"export LEAK=1", `test -z "$LEAK"`} {
		run, err := runner.Run(context.Background(), dir, command)
		if err != nil {
			t.Fatal(err)
		}
		if run.ExitCode != 0 {
			t.Fatalf("%q exited %d: state leaked between checks", command, run.ExitCode)
		}
	}
}

func TestSetupWitnessRefusesTrustAttest(t *testing.T) {
	outside := t.TempDir()
	keyFile := filepath.Join(outside, "witness.key")
	pub, err := attest.GenerateKeyFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	constitutionFile := filepath.Join(outside, "line.toml")
	doc := fmt.Sprintf("[trust]\nlevel = \"attest\"\n\n[[trust.keys]]\nname = \"w\"\npublic_key = %q\n\n[runner]\nkind = \"container\"\nimage = \"x@sha256:abc\"\n\n[[checks.required]]\nname = \"check\"\ncommand = \"true\"\n", attest.EncodePublicKey(pub))
	if err := os.WriteFile(constitutionFile, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	paths, err := workspace.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithConfig(paths, config.Config{Witness: &config.Witness{
		KeyFile: keyFile, Role: "maker", AgentID: "maker-1", Constitution: constitutionFile,
		StateDir: filepath.Join(outside, "state"), Remote: filepath.Join(outside, "remote.git"), Ref: "refs/heads/master",
	}})
	err = server.Serve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not supported by agentsh v1") {
		t.Fatalf("Serve err = %v, want unsupported trust level", err)
	}
}

func TestLineOpsRequireAWitness(t *testing.T) {
	_, client := startTestServer(t)
	err := callLine(t, client, agentrpc.OpLineMerge, nil)
	if err == nil || !strings.Contains(err.Error(), "witness_disabled") {
		t.Fatalf("err = %v, want witness_disabled", err)
	}
}
