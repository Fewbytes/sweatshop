// Package service evaluates readiness predicates for named background
// services: has a port opened, has stdout printed something matching a
// pattern, does an HTTP endpoint answer. It has no dependency on the
// executor package — callers supply small closures (a stdout tail reader)
// so this package stays testable and reusable on its own.
package service

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"time"
)

// Predicate is one condition a service must satisfy to be considered ready.
// Check returning (false, nil) means "not ready yet, keep polling" — err is
// reserved for a predicate that can never succeed as configured (e.g. a bad
// regex), not for the ordinary "connection refused" of a port not open yet.
type Predicate interface {
	Describe() string
	Check(ctx context.Context) (bool, error)
}

// PortPredicate is ready once a TCP connection to Host:Port succeeds.
type PortPredicate struct {
	Host string
	Port int
	// DialTimeout bounds a single connection attempt; it should be well
	// under the poll interval so a hung dial doesn't stall other
	// predicates. Defaults to 500ms.
	DialTimeout time.Duration
}

func (p PortPredicate) Describe() string {
	return fmt.Sprintf("port %s:%d open", p.host(), p.Port)
}

func (p PortPredicate) host() string {
	if p.Host == "" {
		return "localhost"
	}
	return p.Host
}

func (p PortPredicate) Check(ctx context.Context) (bool, error) {
	timeout := p.DialTimeout
	if timeout <= 0 {
		timeout = 500 * time.Millisecond
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", p.host(), p.Port))
	if err != nil {
		return false, nil
	}
	_ = conn.Close()
	return true, nil
}

// StdoutRegexPredicate is ready once Pattern matches somewhere in the last
// TailBytes of a service's stdout. Tail is supplied by the caller (the
// executor knows how to read a running invocation's live output; this
// package doesn't need to).
type StdoutRegexPredicate struct {
	Pattern *regexp.Regexp
	Tail    func() ([]byte, error)
}

func (p StdoutRegexPredicate) Describe() string {
	return fmt.Sprintf("stdout matches /%s/", p.Pattern.String())
}

func (p StdoutRegexPredicate) Check(context.Context) (bool, error) {
	data, err := p.Tail()
	if err != nil {
		// The stream may not have any output yet, or the invocation may not
		// have started writing — treat as "not ready", not a hard failure.
		return false, nil
	}
	return p.Pattern.Match(data), nil
}

// HTTPPredicate is ready once a GET to URL returns a 2xx or 3xx status.
type HTTPPredicate struct {
	URL    string
	Client *http.Client
}

func (p HTTPPredicate) Describe() string {
	return fmt.Sprintf("GET %s returns 2xx/3xx", p.URL)
}

func (p HTTPPredicate) Check(ctx context.Context) (bool, error) {
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return false, err // malformed URL can never succeed — a real error
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, nil
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400, nil
}
