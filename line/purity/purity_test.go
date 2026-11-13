package purity

import (
	"slices"
	"testing"
)

const fixturePath = "example.com/fixture"

func analyzeFixture(t *testing.T) (Report, map[string]FunctionTag) {
	t.Helper()
	report, err := Analyze("testdata/fixture")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]FunctionTag{}
	for _, f := range report.Functions {
		byID[f.ID] = f
	}
	return report, byID
}

func requireTag(t *testing.T, byID map[string]FunctionTag, id string, want Tag) FunctionTag {
	t.Helper()
	got, ok := byID[id]
	if !ok {
		t.Fatalf("function %s not analysed", id)
	}
	if got.Tag != want {
		t.Fatalf("%s tagged %s (chain %v via %q), want %s", id, got.Tag, got.Chain, got.Via, want)
	}
	return got
}

func TestPureFunctionsAreDeterministic(t *testing.T) {
	_, byID := analyzeFixture(t)
	for _, name := range []string{
		"core.Add", "core.Total", "adapter.Format", "adapter.Elapsed", "adapter.IsMissing",
	} {
		requireTag(t, byID, fixturePath+"/"+name, TagDeterministic)
	}
}

func TestDirectEffectsAreTaggedWithTheirSource(t *testing.T) {
	_, byID := analyzeFixture(t)
	cases := []struct {
		fn     string
		source Source
		via    string
	}{
		{"adapter.Now", SourceClock, "time.Now"},
		{"adapter.ReadConfig", SourceFS, "os.ReadFile"},
		{"adapter.Roll", SourceRand, "math/rand.Intn"},
		{"adapter.Fetch", SourceNet, "net/http.Get"},
		{"adapter.HomeDir", SourceEnv, "os.Getenv"},
		{"adapter.Hit", SourceGlobal, "global hits"},
		{"adapter.Peek", SourceGlobal, "global hits"},
		{"adapter.Fill", SourceArgMutation, "arg-mutation dst"},
	}
	for _, c := range cases {
		id := fixturePath + "/" + c.fn
		got := requireTag(t, byID, id, TagNonDeterministic)
		if got.Source != c.source || got.Via != c.via || !slices.Equal(got.Chain, []string{id}) {
			t.Errorf("%s: source %q via %q chain %v, want %q via %q chain [%s]", c.fn, got.Source, got.Via, got.Chain, c.source, c.via, id)
		}
	}
}

func TestCallersOfNonDeterministicFunctionsAreTaintedTransitively(t *testing.T) {
	_, byID := analyzeFixture(t)
	describe := requireTag(t, byID, fixturePath+"/core.Describe", TagNonDeterministic)
	want := []string{
		fixturePath + "/core.Describe", fixturePath + "/core.Stamp",
		fixturePath + "/adapter.Now",
	}
	if !slices.Equal(describe.Chain, want) || describe.Source != SourceClock || describe.Via != "time.Now" {
		t.Fatalf("Describe chain %v source %q via %q, want chain %v clock via time.Now", describe.Chain, describe.Source, describe.Via, want)
	}
	method := requireTag(t, byID, fixturePath+"/adapter.Ticker.Elapsed", TagNonDeterministic)
	if len(method.Chain) != 2 || method.Chain[1] != fixturePath+"/adapter.Ticker.tick" {
		t.Fatalf("method call through the receiver not followed: chain %v", method.Chain)
	}
}

func TestLogicPackageImportingAdapterIsAViolation(t *testing.T) {
	report, _ := analyzeFixture(t)
	want := []Violation{{
		Package: fixturePath + "/core",
		Imports: fixturePath + "/adapter",
		Calls:   []string{fixturePath + "/adapter.Now"},
	}}
	if len(report.Violations) != 1 || report.Violations[0].Package != want[0].Package ||
		report.Violations[0].Imports != want[0].Imports || !slices.Equal(report.Violations[0].Calls, want[0].Calls) {
		t.Fatalf("violations %+v, want %+v (the main package is a composition root and is exempt)", report.Violations, want)
	}
}

func TestPackageSummaryFlagsMixedAdapter(t *testing.T) {
	report, _ := analyzeFixture(t)
	byPath := map[string]PackageSummary{}
	for _, p := range report.Packages {
		byPath[p.Path] = p
	}
	adapter := byPath[fixturePath+"/adapter"]
	if adapter.Role != RoleAdapter || !adapter.Mixed || adapter.DirectEffects == 0 || adapter.Deterministic == 0 {
		t.Errorf("adapter summary %+v, want an adapter mixing direct effects and deterministic functions", adapter)
	}
	core := byPath[fixturePath+"/core"]
	if core.Role != RoleLogic || core.Mixed || core.NonDeterministic != 2 {
		t.Errorf("core summary %+v, want a logic package with 2 inherited non-deterministic functions", core)
	}
}

func TestAnalyzeFailsWithoutModule(t *testing.T) {
	if _, err := Analyze(t.TempDir()); err == nil {
		t.Fatal("Analyze of a tree without go.mod succeeded, want an error")
	}
}
