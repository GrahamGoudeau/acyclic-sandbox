package synth

import (
	"testing"
)

func TestBuildCounts(t *testing.T) {
	s := Build(Anchor)
	if len(s.Patients) != len(PROFILES) {
		t.Fatalf("patients %d", len(s.Patients))
	}
	for typ, l := range s.ByType {
		t.Logf("%s: %d", typ, len(l))
	}
	if len(s.Binaries) == 0 {
		t.Fatal("no notes")
	}
	a, b := Build(Anchor), Build(Anchor)
	if len(a.ByID) != len(b.ByID) || a.ByID["Observation/lab-01-009-01"]["valueQuantity"].(R)["value"] != b.ByID["Observation/lab-01-009-01"]["valueQuantity"].(R)["value"] {
		t.Fatal("not deterministic")
	}
}
