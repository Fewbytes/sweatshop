package service

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitReadyZeroPredicatesIsInstant(t *testing.T) {
	// An already-cancelled context proves WaitReady returns without waiting.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WaitReady(ctx, nil, time.Second, 0); err != nil {
		t.Fatal(err)
	}
}

func TestWaitReadyPortPredicateSucceedsOnceListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	predicates := []Predicate{PortPredicate{Host: "127.0.0.1", Port: port}}
	if err := WaitReady(context.Background(), predicates, time.Second, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestWaitReadyPortPredicateTimesOutWhenNothingListens(t *testing.T) {
	// Grab a port, then immediately close it so nothing is listening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	predicates := []Predicate{PortPredicate{Host: "127.0.0.1", Port: port}}
	err = WaitReady(context.Background(), predicates, 150*time.Millisecond, 20*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("expected last-known-state in error, got: %v", err)
	}
}

func TestWaitReadyStdoutRegexPredicate(t *testing.T) {
	var buf atomic.Pointer[[]byte]
	empty := []byte(nil)
	buf.Store(&empty)
	polled := make(chan struct{}, 1)
	tail := func() ([]byte, error) {
		select {
		case polled <- struct{}{}:
		default:
		}
		return *buf.Load(), nil
	}
	predicates, err := BuildPredicates(ReadinessSpec{StdoutRegex: `listening on \d+`}, tail)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- WaitReady(context.Background(), predicates, time.Second, 10*time.Millisecond)
	}()
	<-polled // the predicate has seen the empty tail
	data := []byte("starting up\nlistening on 8080\n")
	buf.Store(&data)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWaitReadyHTTPPredicate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	predicates, err := BuildPredicates(ReadinessSpec{HTTPURL: srv.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := WaitReady(context.Background(), predicates, time.Second, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestWaitReadyMixedPredicatesAreANDed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	var ready atomic.Bool
	polled := make(chan struct{}, 2)
	tail := func() ([]byte, error) {
		select {
		case polled <- struct{}{}:
		default:
		}
		if !ready.Load() {
			return nil, nil
		}
		return []byte("ready-marker"), nil
	}
	predicates, err := BuildPredicates(ReadinessSpec{Port: port, Host: "127.0.0.1", StdoutRegex: "ready-marker"}, tail)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- WaitReady(context.Background(), predicates, time.Second, 10*time.Millisecond)
	}()
	// Port is open from the start, but the regex predicate isn't satisfied
	// yet — WaitReady must not report ready until both are true.
	<-polled
	<-polled
	select {
	case err := <-done:
		t.Fatalf("WaitReady returned early (%v) before the regex predicate was satisfied", err)
	default:
	}
	ready.Store(true)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestBuildPredicatesRejectsInvalidRegex(t *testing.T) {
	if _, err := BuildPredicates(ReadinessSpec{StdoutRegex: "("}, func() ([]byte, error) { return nil, nil }); err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestBuildPredicatesEmptySpecIsEmpty(t *testing.T) {
	predicates, err := BuildPredicates(ReadinessSpec{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(predicates) != 0 {
		t.Fatalf("expected no predicates, got %d", len(predicates))
	}
}

func TestWaitReadyCtxCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	polled := make(chan struct{}, 1)
	predicates := []Predicate{notReadyPredicate{polled: polled}}
	go func() {
		<-polled
		cancel()
	}()
	err := WaitReady(ctx, predicates, time.Minute, 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected error from cancellation")
	}
}

// notReadyPredicate never becomes ready and reports each Check on polled, so a
// test can act once WaitReady is demonstrably mid-wait.
type notReadyPredicate struct{ polled chan<- struct{} }

func (p notReadyPredicate) Describe() string { return "never ready" }

func (p notReadyPredicate) Check(ctx context.Context) (bool, error) {
	select {
	case p.polled <- struct{}{}:
	default:
	}
	return false, nil
}
