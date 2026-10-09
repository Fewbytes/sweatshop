package guard

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/gitx"
)

// FixTrailer tags the commits that may merge while the line is stopped; its
// value is the andon event ID.
const FixTrailer = "Andon-Event"

const (
	Stopped Status = "stopped" // Pull succeeded
	Cleared Status = "cleared" // Clear succeeded
)

const (
	ReasonAlreadyStopped Reason = "already_stopped"
	ReasonNotStopped     Reason = "not_stopped"
	ReasonStaleEvidence  Reason = "stale_evidence" // attested commit is not the current master
	ReasonNoFailure      Reason = "no_failure"     // pull evidence shows no failing required check
)

// AndonEvent is the record stored in the blob the stop ref points at.
type AndonEvent struct {
	Commit       string    `json:"commit"`
	Check        string    `json:"check"` // first failing required check
	ExitCode     int       `json:"exit_code"`
	EvidenceID   string    `json:"evidence_id,omitempty"`
	AgentID      string    `json:"agent_id"`
	Role         string    `json:"role"`
	KeyID        string    `json:"key_id"`
	Constitution string    `json:"constitution"`
	PulledAt     time.Time `json:"pulled_at"`
	// Attestation is the sha256 of the pull envelope's decoded DSSE payload,
	// so the event can be tied back to the signed evidence.
	Attestation string `json:"attestation"`
}

// stopID returns the ID the stop ref points at, "" when the line is running.
func (g Guard) stopID(ctx context.Context) (string, error) {
	exists, err := g.Repo.RefExists(ctx, StopRef)
	if err != nil || !exists {
		return "", err
	}
	id, err := g.Repo.Ref(ctx, StopRef)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", StopRef, err)
	}
	return id, nil
}

// Stopped reports the pulled cord: id is "" when the line is running. An event
// that cannot be decoded (the ref was written by hand) still stops the line,
// with a nil event.
func (g Guard) Stopped(ctx context.Context) (string, *AndonEvent, error) {
	id, err := g.stopID(ctx)
	if err != nil || id == "" {
		return "", nil, err
	}
	// A hand-written ref may point at any object; only a failing read of a
	// real blob is an error worth surfacing.
	kind, err := g.Repo.Git(ctx, "cat-file", "-t", id)
	if err != nil {
		return "", nil, fmt.Errorf("read %s: %w", StopRef, err)
	}
	if kind != "blob" {
		return id, nil, nil
	}
	data, err := g.Repo.ReadBlob(ctx, id)
	if err != nil {
		return "", nil, fmt.Errorf("read andon event %s: %w", id, err)
	}
	var event AndonEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return id, nil, nil
	}
	return id, &event, nil
}

// currentMaster returns the guarded branch tip, or a no_base reject.
func (g Guard) currentMaster(ctx context.Context, commit string) (string, *Result, error) {
	exists, err := g.Repo.RefExists(ctx, g.Ref)
	if err != nil {
		return "", nil, err
	}
	if !exists {
		return "", &Result{Status: Rejected, Reason: ReasonNoBase, Detail: g.Ref + " does not exist", Commit: commit}, nil
	}
	base, err := g.Repo.Ref(ctx, g.Ref)
	if err != nil {
		return "", nil, fmt.Errorf("read %s: %w", g.Ref, err)
	}
	return base, nil, nil
}

func staleEvidence(attested, base string) Result {
	return Result{
		Status: Rejected, Reason: ReasonStaleEvidence, Commit: attested, Base: base,
		Detail: fmt.Sprintf("evidence is for %s, the line is at %s", attested, base),
	}
}

// Pull stops the line. The evidence must be a signed, failing run of the
// current master: anyone holding a trusted key may pull, but only on proof.
func (g Guard) Pull(ctx context.Context, env attest.Envelope) (Result, error) {
	v, rejected, err := g.verify(env, "", "")
	if err != nil {
		return Result{}, err
	}
	if rejected != nil {
		return *rejected, nil
	}
	base, rejected, err := g.currentMaster(ctx, v.commit)
	if err != nil {
		return Result{}, err
	}
	if rejected != nil {
		return *rejected, nil
	}
	if v.commit != base {
		return staleEvidence(v.commit, base), nil
	}

	alreadyStopped := func(id string) Result {
		return Result{Status: Rejected, Reason: ReasonAlreadyStopped, Commit: v.commit, Event: id,
			Detail: "the andon cord is already pulled (event " + id + ")"}
	}
	if id, err := g.stopID(ctx); err != nil {
		return Result{}, err
	} else if id != "" {
		return alreadyStopped(id), nil
	}

	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return Result{}, fmt.Errorf("decode verified payload: %w", err)
	}
	sum := sha256.Sum256(payload)
	predicate := v.statement.Predicate
	event := AndonEvent{
		Attestation: "sha256:" + hex.EncodeToString(sum[:]),
		Commit:      v.commit, AgentID: predicate.AgentID, Role: predicate.Role,
		KeyID: v.keyID, Constitution: predicate.Constitution, PulledAt: g.now().UTC(),
	}
	found := false
	for _, required := range g.Constitution.Checks.Required {
		for _, r := range predicate.Checks {
			if r.Name == required.Name && r.Command == required.Command && r.ExitCode != 0 {
				event.Check, event.ExitCode, event.EvidenceID = r.Name, r.ExitCode, r.EvidenceID
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		return Result{Status: Rejected, Reason: ReasonNoFailure, Commit: v.commit,
			Detail: "the evidence shows no failing required check"}, nil
	}

	data, err := json.Marshal(event)
	if err != nil {
		return Result{}, err
	}
	blob, err := g.Repo.WriteBlob(ctx, data)
	if err != nil {
		return Result{}, err
	}
	if err := g.Repo.UpdateRef(ctx, StopRef, blob, ""); err != nil {
		if errors.Is(err, gitx.ErrRefMoved) {
			id, err := g.stopID(ctx)
			if err != nil {
				return Result{}, err
			}
			return alreadyStopped(id), nil
		}
		return Result{}, err
	}
	return Result{Status: Stopped, Commit: v.commit, Event: blob}, nil
}

// Clear restarts the line. It needs a signed run of the current master that
// passes every required check, so a fix that merged but did not really repair
// the build cannot lift the stop.
func (g Guard) Clear(ctx context.Context, env attest.Envelope) (Result, error) {
	id, err := g.stopID(ctx)
	if err != nil {
		return Result{}, err
	}
	notStopped := Result{Status: Rejected, Reason: ReasonNotStopped, Detail: "the andon cord is not pulled"}
	if id == "" {
		return notStopped, nil
	}
	v, rejected, err := g.verify(env, "", "")
	if err != nil {
		return Result{}, err
	}
	if rejected != nil {
		return *rejected, nil
	}
	base, rejected, err := g.currentMaster(ctx, v.commit)
	if err != nil {
		return Result{}, err
	}
	if rejected != nil {
		return *rejected, nil
	}
	if v.commit != base {
		return staleEvidence(v.commit, base), nil
	}
	// The pulled-on commit's own merge attestation passes every check, so
	// replaying it would clear the line with no fix; demand a newer master.
	_, event, err := g.Stopped(ctx)
	if err != nil {
		return Result{}, err
	}
	if event != nil && v.commit == event.Commit {
		r := staleEvidence(v.commit, base)
		r.Detail = "clearing needs a passing run on a master newer than the failing commit"
		return r, nil
	}
	if reason, detail := checkResults(g.Constitution.Checks.Required, v.statement.Predicate.Checks); reason != "" {
		return Result{Status: Rejected, Reason: reason, Detail: detail, Commit: v.commit, Base: base}, nil
	}
	if err := g.Repo.DeleteRefIf(ctx, StopRef, id); err != nil {
		if errors.Is(err, gitx.ErrRefMoved) {
			notStopped.Commit = v.commit
			return notStopped, nil
		}
		return Result{}, err
	}
	return Result{Status: Cleared, Commit: v.commit, Base: base, Event: id}, nil
}
