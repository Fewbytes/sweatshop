package jvmexpander

import (
	"os"
	"path/filepath"
	"testing"
)

func TestName(t *testing.T) {
	if got := (Expander{}).Name(); got != "jvm" {
		t.Errorf("Name() = %q, want jvm", got)
	}
}

func TestExpandReturnsNothingWithoutLockfile(t *testing.T) {
	nodes, edges, err := (Expander{}).Expand(t.TempDir())
	if err != nil || nodes != nil || edges != nil {
		t.Errorf("Expand(no lockfile) = %v, %v, %v, want nil, nil, nil", nodes, edges, err)
	}
}

func TestExpandListsCoordinatesFromFixtureLockfile(t *testing.T) {
	nodes, edges, err := (Expander{}).Expand("testdata/fixtures/root")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("edges = %v, want none (gradle.lockfile records no edges)", edges)
	}
	want := []string{"jvm:com.google.guava:guava@33.0.0-jre", "jvm:org.slf4j:slf4j-api@2.0.9"}
	if len(nodes) != len(want) {
		t.Fatalf("nodes = %v, want keys %v", nodes, want)
	}
	for i, n := range nodes {
		if n.Key != want[i] || n.Kind != "package" {
			t.Errorf("nodes[%d] = %+v, want package %s", i, n, want[i])
		}
	}
}

func TestExpandRejectsMalformedCoordinate(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "gradle.lockfile"), []byte("not-a-coordinate=compileClasspath\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (Expander{}).Expand(root); err == nil {
		t.Fatal("Expand(malformed line) succeeded, want error")
	}
}
