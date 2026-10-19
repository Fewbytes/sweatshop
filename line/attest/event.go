package attest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// EventPredicateType marks a command-event statement: one agentsh command
// execution, a function of its input tree (design-docs/agentsh-executors.md).
const EventPredicateType = "https://github.com/Fewbytes/sweatshop/line/attest/command-event/v0"

// CommandEventPredicate records one command execution. Everything that
// determines the result is here: the input tree is the statement subject, and
// Command, Env and Capabilities are the other inputs of the function.
type CommandEventPredicate struct {
	Command      string            `json:"command"`
	Env          map[string]string `json:"env,omitempty"`
	Capabilities []string          `json:"capabilities"`
	// Executor names the executor that ran the command; it is the party a
	// future signature binds.
	Executor string `json:"executor"`
	ExitCode int    `json:"exit_code"`
	// OutputBlobs maps a stream name (stdout, stderr) to the digest of its
	// blob in the record plane's blob store.
	OutputBlobs map[string]string `json:"output_blobs,omitempty"`
	// ResultTree is the git tree of the workspace after the command; equal to
	// the input tree when the command changed nothing.
	ResultTree string    `json:"result_tree"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// CommandEventStatement is the in-toto statement for a command event. Its
// subject is the input tree.
type CommandEventStatement struct {
	Type          string                `json:"_type"`
	Subject       []Subject             `json:"subject"`
	PredicateType string                `json:"predicateType"`
	Predicate     CommandEventPredicate `json:"predicate"`
}

// NewCommandEventStatement binds the event to the tree the command ran on.
func NewCommandEventStatement(inputTree string, predicate CommandEventPredicate) CommandEventStatement {
	return CommandEventStatement{
		Type:          StatementType,
		Subject:       []Subject{{Name: TreeSubjectName, Digest: map[string]string{TreeDigestAlgorithm: inputTree}}},
		PredicateType: EventPredicateType,
		Predicate:     predicate,
	}
}

// InputTree returns the git tree the command ran on.
func (s CommandEventStatement) InputTree() (string, error) {
	for _, subject := range s.Subject {
		if subject.Name == TreeSubjectName && subject.Digest[TreeDigestAlgorithm] != "" {
			return subject.Digest[TreeDigestAlgorithm], nil
		}
	}
	return "", fmt.Errorf("event statement has no %s subject", TreeSubjectName)
}

// UnsignedEventEnvelope wraps the statement in a DSSE envelope with no
// signatures. Event attestations are unsigned until executor keys are bound in
// the constitution; the envelope is already the final shape, so adding
// signatures changes no consumer.
func UnsignedEventEnvelope(statement CommandEventStatement) (Envelope, error) {
	payload, err := json.Marshal(statement)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{
		PayloadType: PayloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures:  []Signature{},
	}, nil
}

// ReadUnsignedEventEnvelope decodes and validates an event envelope. It
// checks shape only: nothing here proves who ran the command, and callers must
// not treat the result as authority.
func ReadUnsignedEventEnvelope(env Envelope) (CommandEventStatement, error) {
	if env.PayloadType != PayloadType {
		return CommandEventStatement{}, fmt.Errorf("payload type %q, want %q", env.PayloadType, PayloadType)
	}
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return CommandEventStatement{}, fmt.Errorf("decode payload: %w", err)
	}
	var statement CommandEventStatement
	if err := json.Unmarshal(payload, &statement); err != nil {
		return CommandEventStatement{}, fmt.Errorf("decode event statement: %w", err)
	}
	if statement.Type != StatementType || statement.PredicateType != EventPredicateType {
		return CommandEventStatement{}, fmt.Errorf("unexpected statement %q with predicate %q", statement.Type, statement.PredicateType)
	}
	if _, err := statement.InputTree(); err != nil {
		return CommandEventStatement{}, err
	}
	return statement, nil
}
