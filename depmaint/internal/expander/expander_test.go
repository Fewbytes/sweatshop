package expander

import "testing"

type fakeExpander struct{ name string }

func (f fakeExpander) Name() string      { return f.name }
func (f fakeExpander) Available() bool   { return true }
func (f fakeExpander) Expand(string) ([]Node, []Edge, error) {
	return nil, nil, nil
}

func TestRegisterAndAll(t *testing.T) {
	resetForTest()
	Register(fakeExpander{name: "test-a"})
	Register(fakeExpander{name: "test-b"})
	all := All()
	if len(all) != 2 {
		t.Fatalf("All() returned %d expanders, want 2", len(all))
	}
	if all[0].Name() != "test-a" || all[1].Name() != "test-b" {
		t.Errorf("All() order = %q, %q", all[0].Name(), all[1].Name())
	}
}

func TestAllEmptyByDefault(t *testing.T) {
	resetForTest()
	if got := All(); len(got) != 0 {
		t.Errorf("All() = %v, want empty", got)
	}
}
