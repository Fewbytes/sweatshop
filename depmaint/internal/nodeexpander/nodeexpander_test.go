package nodeexpander

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
)

const fixture = "testdata/fixtures/root"

func TestName(t *testing.T) {
	if got := (Expander{}).Name(); got != "node" {
		t.Errorf("Name() = %q, want node", got)
	}
}

func TestExpandReturnsNothingWithoutLockfile(t *testing.T) {
	nodes, edges, err := (Expander{}).Expand(t.TempDir())
	if err != nil || nodes != nil || edges != nil {
		t.Errorf("Expand(no lockfile) = %v, %v, %v, want nil, nil, nil", nodes, edges, err)
	}
}

func TestExpandRejectsLockfileVersion1(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package-lock.json"), []byte(`{"lockfileVersion":1,"dependencies":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (Expander{}).Expand(root); err == nil {
		t.Fatal("Expand(lockfileVersion 1) succeeded, want error")
	}
}

func TestExpandFailsOnMissingRequiredDependency(t *testing.T) {
	root := t.TempDir()
	lock := `{"lockfileVersion":3,"packages":{"":{"name":"r","version":"1.0.0","dependencies":{"ghost":"^1"}}}}`
	if err := os.WriteFile(filepath.Join(root, "package-lock.json"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (Expander{}).Expand(root); err == nil {
		t.Fatal("Expand with unresolvable dependency succeeded, want error")
	}
}

func TestMainModulePath(t *testing.T) {
	got, err := MainModulePath(fixture)
	if err != nil || got != "roottest" {
		t.Errorf("MainModulePath = %q, %v, want roottest", got, err)
	}
}

func TestExpandEmitsNodesWithDirectFlags(t *testing.T) {
	nodes, _, err := (Expander{}).Expand(fixture)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	want := map[string]bool{ // key -> Direct
		"node:left-pad@1.3.0": true,
		"node:left-pad@0.9.1": false, // nested copy only wrapper asked for
		"node:wrapper@2.0.0":  true,
		"node:localdep@0.1.0": true,
	}
	got := map[string]bool{}
	for _, n := range nodes {
		if n.Kind != "package" {
			t.Errorf("%s Kind = %q, want package", n.Key, n.Kind)
		}
		got[n.Key] = n.Direct
	}
	if len(got) != len(want) {
		t.Errorf("nodes = %v, want %v", got, want)
	}
	for k, direct := range want {
		if d, ok := got[k]; !ok || d != direct {
			t.Errorf("node %s: present=%v direct=%v, want direct=%v", k, ok, d, direct)
		}
	}
}

func TestExpandResolvesNestedNodeModulesBeforeHoisted(t *testing.T) {
	_, edges, err := (Expander{}).Expand(fixture)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	want := expander.Edge{From: "node:wrapper@2.0.0", To: "node:left-pad@0.9.1", Requirement: "^0.9.0", Resolved: "0.9.1"}
	for _, e := range edges {
		if e == want {
			return
		}
	}
	t.Fatalf("missing edge %+v in %+v", want, edges)
}

func TestExpandFlagsFileDependencyAsInternal(t *testing.T) {
	_, edges, err := (Expander{}).Expand(fixture)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	var sawInternal bool
	for _, e := range edges {
		isLocal := e.To == "node:localdep@0.1.0"
		if e.Internal != isLocal {
			t.Errorf("edge %+v: Internal=%v, want %v", e, e.Internal, isLocal)
		}
		sawInternal = sawInternal || isLocal
	}
	if !sawInternal {
		t.Fatalf("no edge to node:localdep@0.1.0 in %+v", edges)
	}
}
