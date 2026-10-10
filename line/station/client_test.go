package station

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fastBusy(t *testing.T) {
	i, m, g := busyInitial, busyMax, busyGiveUp
	busyInitial, busyMax, busyGiveUp = time.Millisecond, 4*time.Millisecond, time.Minute
	t.Cleanup(func() { busyInitial, busyMax, busyGiveUp = i, m, g })
}

func busyServer(n int32, status int, msg string, calls *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= n {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
}

func TestDoRetriesBusy(t *testing.T) {
	fastBusy(t)
	var calls atomic.Int32
	ts := busyServer(3, http.StatusConflict, "busy", &calls)
	defer ts.Close()
	var out struct{ OK bool }
	_, err := Client{URL: ts.URL}.do(context.Background(), http.MethodPost, "/v1/merge", nil, strings.NewReader("body"), &out)
	if err != nil || !out.OK || calls.Load() != 4 {
		t.Fatalf("err=%v ok=%v calls=%d", err, out.OK, calls.Load())
	}
}

func TestDoOtherErrorsNotRetried(t *testing.T) {
	fastBusy(t)
	for _, tc := range []struct {
		status int
		msg    string
	}{{http.StatusConflict, "base_moved"}, {http.StatusInternalServerError, "busy"}} {
		var calls atomic.Int32
		ts := busyServer(5, tc.status, tc.msg, &calls)
		_, err := Client{URL: ts.URL}.do(context.Background(), http.MethodPost, "/v1/merge", nil, nil, nil)
		ts.Close()
		if err == nil || calls.Load() != 1 {
			t.Fatalf("%d %s: err=%v calls=%d", tc.status, tc.msg, err, calls.Load())
		}
	}
}

func TestDoBusyGivesUpAndCancels(t *testing.T) {
	fastBusy(t)
	var calls atomic.Int32
	ts := busyServer(1<<30, http.StatusConflict, "busy", &calls)
	defer ts.Close()
	busyGiveUp = 20 * time.Millisecond
	if _, err := (Client{URL: ts.URL}).do(context.Background(), http.MethodPost, "/x", nil, nil, nil); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("want busy error, got %v", err)
	}
	busyGiveUp = 100 * time.Hour
	busyInitial, busyMax = time.Hour, time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := (Client{URL: ts.URL}).do(ctx, http.MethodPost, "/x", nil, nil, nil); err != context.DeadlineExceeded {
		t.Fatalf("want ctx error, got %v", err)
	}
}
