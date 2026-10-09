package attest

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"time"
)

const (
	PayloadType           = "application/vnd.in-toto+json"
	StatementType         = "https://in-toto.io/Statement/v1"
	ChecksPredicateType   = "https://github.com/Fewbytes/sweatshop/line/attest/checks/v0"
	CommitSubjectName     = "git-commit"
	CommitDigestAlgorithm = "gitCommit"
	TreeSubjectName       = "git-tree"
	TreeDigestAlgorithm   = "gitTree"
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

// NewStatement binds the predicate to a commit, not only its tree. A role's
// authority covers a change (the commit against its parent); a tree-only
// subject would let an old attestation authorize any later commit that
// restores the same tree. The tree subject is informational.
func NewStatement(commit, tree string, predicate ChecksPredicate) Statement {
	return Statement{
		Type: StatementType,
		Subject: []Subject{
			{Name: CommitSubjectName, Digest: map[string]string{CommitDigestAlgorithm: commit}},
			{Name: TreeSubjectName, Digest: map[string]string{TreeDigestAlgorithm: tree}},
		},
		PredicateType: ChecksPredicateType,
		Predicate:     predicate,
	}
}

func (s Statement) Commit() (string, error) {
	return s.subject(CommitSubjectName, CommitDigestAlgorithm)
}

func (s Statement) Tree() (string, error) {
	return s.subject(TreeSubjectName, TreeDigestAlgorithm)
}

func (s Statement) subject(name, algorithm string) (string, error) {
	for _, subject := range s.Subject {
		if subject.Name == name && subject.Digest[algorithm] != "" {
			return subject.Digest[algorithm], nil
		}
	}
	return "", fmt.Errorf("statement has no %s subject", name)
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
