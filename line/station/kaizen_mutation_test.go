package station

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/linestore"
)

func TestKaizenMutationRunsAreRecordedAndListedOldestFirst(t *testing.T) {
	r := newSignalsRig(t)
	ctx := context.Background()
	score, size := 0.75, 8
	want := []linestore.MutationRun{
		{Commit: signalsCommitA, Source: linestore.MutationFromKaizenRun, Language: "go", Scope: []string{"pkg"}, Tool: "gremlins", ToolVersion: "v0.5.1", DurationMS: 61000, SampleSize: &size, Score: &score},
		{Commit: signalsCommitB, Source: linestore.MutationFromLandedCard, Language: "python", Scope: []string{"a.py"}, Tool: "mutmut", DurationMS: 4, Unavailable: "mutmut is not installed"},
	}
	for _, m := range want {
		if err := r.kaizen.RecordKaizenMutation(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.kaizen.KaizenMutationRuns(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("runs = %+v, %v; want %+v", got, err, want)
	}
}

func TestKaizenMutationRunRefusesAScoreThatIsAlsoUnavailable(t *testing.T) {
	r := newSignalsRig(t)
	score, size := 0.5, 2
	bad := linestore.MutationRun{Commit: signalsCommitA, Source: linestore.MutationFromKaizenRun, Language: "go", Scope: []string{"pkg"}, Tool: "gremlins",
		SampleSize: &size, Score: &score, Unavailable: "also gone"}
	err := r.kaizen.RecordKaizenMutation(context.Background(), bad)
	if err == nil || !strings.Contains(err.Error(), "either a score or the reason") {
		t.Fatalf("err = %v", err)
	}
}

func TestKaizenMutationRunsAreForTheKaizenRoleOnly(t *testing.T) {
	r := newSignalsRig(t)
	if _, err := r.mkr.KaizenMutationRuns(context.Background()); err == nil || !strings.Contains(err.Error(), "may not read") {
		t.Fatalf("maker read: %v", err)
	}
}

func TestMutationRunFromSignalsKeepsUnavailableApartFromZero(t *testing.T) {
	scored, err := MutationRunFromSignals(signalsCommitA, linestore.MutationFromKaizenRun,
		[]byte(`{"language":"go","scope":["pkg"],"tool":"gremlins","tool_version":"v0.5.1","mutation_score":0,"sample_size":4,"duration_ms":900}`))
	if err != nil || scored.Score == nil || *scored.Score != 0 || *scored.SampleSize != 4 || scored.ToolVersion != "v0.5.1" || scored.Unavailable != "" {
		t.Fatalf("scored = %+v, %v", scored, err)
	}
	gone, err := MutationRunFromSignals(signalsCommitA, linestore.MutationFromLandedCard,
		[]byte(`{"language":"go","scope":["pkg"],"tool":"gremlins","tool_version":{"unavailable":"gremlins is not installed"},"mutation_score":{"unavailable":"gremlins is not installed"},"sample_size":{"unavailable":"gremlins is not installed"},"duration_ms":2}`))
	if err != nil || gone.Score != nil || gone.SampleSize != nil || gone.ToolVersion != "" || gone.Unavailable != "gremlins is not installed" {
		t.Fatalf("gone = %+v, %v", gone, err)
	}
	if _, err := MutationRunFromSignals(signalsCommitA, linestore.MutationFromKaizenRun, []byte(`{"language":"go"}`)); err == nil {
		t.Fatal("mutation.json without duration_ms was accepted")
	}
}
