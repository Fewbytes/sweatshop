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
	// The session must be unique per checkout: session state carries the CWD,
	// and request.CWD only seeds a new session, so a shared one would cd into
	// a previous checkout and attest the wrong tree.
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
	result, mergeErr := s.witness.Merge(ctx, s.paths.Root, rev, s.Config.Witness.Remote, s.guard)
	var recordErr error
	if result.Attestation.Commit != "" {
		recordErr = s.recordAttestation(ctx, result.Attestation)
	}
	return mergeResponse(request.ID, result, mergeErr, recordErr), nil
}

// mergeResponse decides the reply to line_merge. A recording failure after a
// successful Merge must not look like a failed merge: the guard may already
// have advanced the ref, and a caller that retried would act on a landed merge.
func mergeResponse(id string, result witness.MergeResult, mergeErr, recordErr error) agentrpc.Response {
	if mergeErr != nil {
		return agentrpc.Failure(id, "merge", mergeErr.Error())
	}
	if recordErr != nil {
		return agentrpc.Failure(id, "attestation_not_recorded", fmt.Sprintf(
			"merge completed (guard status %q, commit %s) but the attestation was not recorded: %v",
			result.Guard.Status, result.Guard.Commit, recordErr))
	}
	return agentrpc.Success(id, result)
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
	tree, err := result.Statement.Tree()
	if err != nil {
		return err
	}
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
