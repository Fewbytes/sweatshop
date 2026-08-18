package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ReadinessSpec configures which predicates a service must satisfy, and how
// long to wait for them. Fields left at their zero value are omitted:
// Port == 0 means no port predicate, StdoutRegex == "" means no regex
// predicate, HTTPURL == "" means no HTTP predicate. A spec with none of
// these set is trivially, immediately ready.
type ReadinessSpec struct {
	Port        int
	Host        string
	StdoutRegex string
	TailBytes   int

	HTTPURL string

	Timeout      time.Duration
	PollInterval time.Duration
}

const (
	DefaultTailBytes    = 4096
	DefaultPollInterval = 250 * time.Millisecond
)

// BuildPredicates compiles spec into the ordered predicate list Evaluate
// checks: port first (fast, cheap), then stdout regex, then HTTP — the
// "progressive" order from the design, cheapest and least dependent checks
// first. tail supplies a stdout regex predicate's data; it's ignored if no
// StdoutRegex is configured. A malformed regex is a real configuration
// error, returned immediately rather than surfacing as a readiness timeout.
func BuildPredicates(spec ReadinessSpec, tail func() ([]byte, error)) ([]Predicate, error) {
	var predicates []Predicate
	if spec.Port > 0 {
		predicates = append(predicates, PortPredicate{Host: spec.Host, Port: spec.Port})
	}
	if strings.TrimSpace(spec.StdoutRegex) != "" {
		pattern, err := regexp.Compile(spec.StdoutRegex)
		if err != nil {
			return nil, fmt.Errorf("invalid stdout readiness regex: %w", err)
		}
		if tail == nil {
			return nil, errors.New("stdout readiness regex configured with no tail source")
		}
		predicates = append(predicates, StdoutRegexPredicate{Pattern: pattern, Tail: tail})
	}
	if strings.TrimSpace(spec.HTTPURL) != "" {
		predicates = append(predicates, HTTPPredicate{URL: spec.HTTPURL})
	}
	return predicates, nil
}

// WaitReady polls predicates in order, short-circuiting each round at the
// first one that isn't ready yet (so, e.g., an HTTP check never fires while
// the port predicate ahead of it is still failing). It returns nil once
// every predicate has passed in the same round, and a descriptive error —
// naming which predicates were last seen ready vs not — on timeout or ctx
// cancellation. Zero predicates is instant success.
func WaitReady(ctx context.Context, predicates []Predicate, timeout, pollInterval time.Duration) error {
	if len(predicates) == 0 {
		return nil
	}
	if pollInterval <= 0 {
		pollInterval = DefaultPollInterval
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	deadline := time.Now().Add(timeout)
	last := make([]bool, len(predicates))

	for {
		allReady := true
		for i, p := range predicates {
			ready, err := p.Check(ctx)
			if err != nil {
				return fmt.Errorf("readiness predicate %q: %w", p.Describe(), err)
			}
			last[i] = ready
			if !ready {
				allReady = false
				break // progressive: don't check later, more expensive predicates yet
			}
		}
		if allReady {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("readiness timeout after %s: %s", timeout, describeState(predicates, last))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("readiness cancelled: %s: %w", describeState(predicates, last), ctx.Err())
		case <-time.After(minDuration(pollInterval, time.Until(deadline))):
		}
	}
}

func describeState(predicates []Predicate, last []bool) string {
	var parts []string
	for i, p := range predicates {
		status := "not ready"
		if last[i] {
			status = "ready"
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", p.Describe(), status))
	}
	return strings.Join(parts, ", ")
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	if b <= 0 {
		return a
	}
	return b
}
