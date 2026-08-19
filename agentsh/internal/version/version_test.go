package version

import "testing"

func TestStringOmitsUnknownCommit(t *testing.T) {
	Version, Commit = "1.2.3", "none"
	if got := String(); got != "1.2.3" {
		t.Fatalf("String() = %q, want %q", got, "1.2.3")
	}
	Version, Commit = "1.2.3", ""
	if got := String(); got != "1.2.3" {
		t.Fatalf("String() = %q, want %q", got, "1.2.3")
	}
}

func TestStringIncludesRealCommit(t *testing.T) {
	Version, Commit = "1.2.3", "abc1234"
	want := "1.2.3 (abc1234)"
	if got := String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	Version, Commit = "dev", "none" // restore defaults for other tests
}
