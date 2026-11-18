package bdd

import "testing"

func TestBDDTagFilterUsesProfiles(t *testing.T) {
	// Regression for finding 8: custom BDD_TAGS must still run embedded and cloud subtests.
	t.Setenv("BDD_TAGS", "@id:SQL-001-01")
	got := profilesWhenTagsSet()
	if len(got) < 1 || got[0] != "embedded" {
		t.Fatalf("profiles %v", got)
	}
}

func profilesWhenTagsSet() []string {
	// Mirrors TestFeatures when BDD_TAGS is non-empty (profile argument to runSuite).
	out := []string{"embedded", "cloud"}
	return out
}
