package guidance

import (
	"slices"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/constitution"
)

func TestGuidesEmbedded(t *testing.T) {
	if !strings.HasPrefix(Writer, "# Writer Guide") {
		t.Errorf("Writer starts %q, want the writer guide", Writer[:min(40, len(Writer))])
	}
	if !strings.HasPrefix(Kaizen, "# Kaizen Guide") {
		t.Errorf("Kaizen starts %q, want the kaizen guide", Kaizen[:min(40, len(Kaizen))])
	}
}

// A new constitution role must get an explicit guidance decision.
func TestEveryConstitutionRoleHasAnEntry(t *testing.T) {
	for _, r := range constitution.Roles {
		if _, ok := For(r); !ok {
			t.Errorf("role %q has no entry in byRole", r)
		}
	}
	for r := range byRole {
		if !slices.Contains(constitution.Roles, r) {
			t.Errorf("byRole has %q, which is not a constitution role", r)
		}
	}
}

func TestCodeWritingRolesGetWriterGuide(t *testing.T) {
	for _, r := range []string{"maker", "andon", "gemba"} {
		if g, _ := For(r); g != Writer {
			t.Errorf("For(%q) is not exactly the writer guide", r)
		}
	}
}

func TestKaizenGetsWriterThenKaizenGuide(t *testing.T) {
	g, _ := For("kaizen")
	if g != Writer+"\n\n"+Kaizen {
		t.Errorf("For(kaizen) is not writer guide followed by kaizen guide")
	}
}

func TestViceroyGetsNoGuidance(t *testing.T) {
	if g, ok := For("viceroy"); !ok || g != "" {
		t.Errorf("For(viceroy) = %d chars, %v; want empty, true", len(g), ok)
	}
}

func TestUnknownRole(t *testing.T) {
	if g, ok := For("janitor"); ok || g != "" {
		t.Errorf("For(janitor) = %d chars, %v; want empty, false", len(g), ok)
	}
}
