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
	start := time.Now()
	if err := WaitReady(context.Background(), nil, time.Second, 0); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("zero predicates took %v, want near-instant", elapsed)
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
	tail := func() ([]byte, error) { return *buf.Load(), nil }
	predicates, err := BuildPredicates(ReadinessSpec{StdoutRegex: `listening on \d+`}, tail)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- WaitReady(context.Background(), predicates, time.Second, 10*time.Millisecond)
	}()
	time.Sleep(30 * time.Millisecond)
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
	tail := func() ([]byte, error) {
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
	select {
	case err := <-done:
		t.Fatalf("WaitReady returned early (%v) before the regex predicate was satisfied", err)
	case <-time.After(60 * time.Millisecond):
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
	predicates := []Predicate{PortPredicate{Host: "127.0.0.1", Port: 1}} // nothing listens on port 1
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := WaitReady(ctx, predicates, time.Minute, 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected error from cancellation")
	}
}
