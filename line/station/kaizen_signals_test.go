package station

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	signalsCommitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	signalsCommitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func newSignalsRig(t *testing.T) *kaizenRig {
	t.Helper()
	r := newKaizenRig(t)
	r.srv.Store = openTestStore(t)
	return r
}

func TestKaizenSignalsAreStoredNextToTheCommitsCIReports(t *testing.T) {
	r := newSignalsRig(t)
	prefix, err := r.kaizen.PublishKaizenSignals(context.Background(), KaizenSignals{
		Commit: signalsCommitA, Signals: json.RawMessage(`{"repo":{}}`), After: json.RawMessage(`{"repo":{"x":1}}`), ReportHTML: "<p>status</p>",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "reports/kaizen/" + signalsCommitA; prefix != want {
		t.Fatalf("prefix %q, want %q", prefix, want)
	}
	for name, want := range map[string]string{"signals.json": `{"repo":{}}`, "signals-after.json": `{"repo":{"x":1}}`, "report.html": "<p>status</p>"} {
		got, err := os.ReadFile(filepath.Join(r.reportsDir, filepath.FromSlash(prefix), name))
		if err != nil || string(got) != want {
			t.Errorf("%s: %q, %v; want %q", name, got, err, want)
		}
	}
}

func TestKaizenPreviousSignalsAreTheNewestBaselineOfAnotherCommit(t *testing.T) {
	r := newSignalsRig(t)
	ctx := context.Background()
	if prev, err := r.kaizen.PreviousKaizenSignals(ctx, signalsCommitB); err != nil || prev != nil {
		t.Fatalf("empty history: %+v, %v", prev, err)
	}
	publish := func(commit, signals string) {
		t.Helper()
		if _, err := r.kaizen.PublishKaizenSignals(ctx, KaizenSignals{Commit: commit, Signals: json.RawMessage(signals), ReportHTML: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	publish(signalsCommitA, `{"n":1}`)
	publish(signalsCommitB, `{"n":2}`)
	publish(signalsCommitB, `{"n":2}`) // the run publishes again after its change
	prev, err := r.kaizen.PreviousKaizenSignals(ctx, signalsCommitB)
	if err != nil || prev == nil || prev.Commit != signalsCommitA || string(prev.Signals) != `{"n":1}` {
		t.Fatalf("previous of B: %+v, %v", prev, err)
	}
	if prev, err = r.kaizen.PreviousKaizenSignals(ctx, signalsCommitA); err != nil || prev == nil || prev.Commit != signalsCommitB {
		t.Fatalf("previous of A: %+v, %v", prev, err)
	}
	if prev, err := r.kaizen.PreviousKaizenSignals(ctx, "c"+signalsCommitA[1:]); err != nil || prev == nil || prev.Commit != signalsCommitB || string(prev.Signals) != `{"n":2}` {
		t.Fatalf("history after republishing B: %+v, %v; want B once, with its first signals", prev, err)
	}
}

func TestKaizenSignalsRejectBadBodies(t *testing.T) {
	r := newSignalsRig(t)
	ctx := context.Background()
	good := KaizenSignals{Commit: signalsCommitA, Signals: json.RawMessage(`{}`), ReportHTML: "x"}
	for name, mutate := range map[string]func(*KaizenSignals){
		"short commit":       func(k *KaizenSignals) { k.Commit = "abc" },
		"path in commit":     func(k *KaizenSignals) { k.Commit = "../" + signalsCommitA },
		"signals not object": func(k *KaizenSignals) { k.Signals = json.RawMessage(`[1]`) },
		"signals missing":    func(k *KaizenSignals) { k.Signals = nil },
		"after not object":   func(k *KaizenSignals) { k.After = json.RawMessage(`"x"`) },
		"empty report":       func(k *KaizenSignals) { k.ReportHTML = "" },
	} {
		k := good
		mutate(&k)
		if _, err := r.kaizen.PublishKaizenSignals(ctx, k); err == nil || !strings.Contains(err.Error(), "400") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestKaizenSignalsAreKaizenOnlyAndNeedAStore(t *testing.T) {
	r := newSignalsRig(t)
	ctx := context.Background()
	k := KaizenSignals{Commit: signalsCommitA, Signals: json.RawMessage(`{}`), ReportHTML: "x"}
	if _, err := r.mkr.PublishKaizenSignals(ctx, k); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("maker publish: %v", err)
	}
	if _, err := r.mkr.PreviousKaizenSignals(ctx, signalsCommitA); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("maker read: %v", err)
	}
	r.srv.Store = nil
	if _, err := r.kaizen.PublishKaizenSignals(ctx, k); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("no history: %v", err)
	}
}
