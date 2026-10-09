package guard

import (
	"fmt"
	"slices"

	"github.com/Fewbytes/sweatshop/line/attest"
)

// verified is an attestation that passed every check that does not depend on
// what the caller wants to do with it.
type verified struct {
	statement attest.Statement
	keyID     string
	commit    string // the commit the attestation is bound to
}

// verify is shared by Merge, Pull and Clear: trusted key, key-role binding,
// commit subject, constitution digest, runner. A non-empty wantCommit must
// equal the attested commit (Merge's candidate); it is checked before the
// constitution so the order of reasons is the same for every caller. On a
// reject the returned Result is non-nil, with Commit set to rejectCommit.
func (g Guard) verify(env attest.Envelope, wantCommit, rejectCommit string) (*verified, *Result, error) {
	reject := func(reason Reason, detail string) (*verified, *Result, error) {
		return nil, &Result{Status: Rejected, Reason: reason, Detail: detail, Commit: rejectCommit}, nil
	}
	trusted, err := g.Constitution.TrustedKeys()
	if err != nil {
		return nil, nil, err
	}
	keyRoles, err := g.Constitution.KeyRoles()
	if err != nil {
		return nil, nil, err
	}
	statement, keyID, err := attest.VerifyStatement(env, trusted)
	if err != nil {
		return reject(ReasonUntrusted, err.Error())
	}
	if allowed := keyRoles[keyID]; len(allowed) > 0 && !slices.Contains(allowed, statement.Predicate.Role) {
		return reject(ReasonRoleNotPermitted, fmt.Sprintf("the verifying key may only attest as %v, not %s", allowed, statement.Predicate.Role))
	}
	// Bind to the commit, not its tree: role authority covers a change, and a
	// tree-only match would let an old attestation authorize a later commit
	// that restores the same tree (reverting newer work under the old role).
	attestedCommit, err := statement.Commit()
	if err != nil {
		return reject(ReasonUntrusted, err.Error())
	}
	if wantCommit != "" && attestedCommit != wantCommit {
		return reject(ReasonCommitMismatch, fmt.Sprintf("candidate %s, attested commit %s", wantCommit, attestedCommit))
	}
	predicate := statement.Predicate
	if predicate.Constitution != g.ConstitutionDigest {
		return reject(ReasonConstitution, fmt.Sprintf("attested under %s, line is at %s", predicate.Constitution, g.ConstitutionDigest))
	}
	if want := g.Constitution.Runner.ID(); predicate.Runner != want {
		return reject(ReasonRunner, fmt.Sprintf("checks ran on %s, constitution requires %s", predicate.Runner, want))
	}
	return &verified{statement: statement, keyID: keyID, commit: attestedCommit}, nil, nil
}
