package gate

import "testing"

func TestGoModPatchBumpConsistency(t *testing.T) {
	rules := ReviewRules{
		GoModPatchBump: []string{"go.mod", "**/go.mod"},
		Tiers:          map[string][]string{"high": {"Makefile"}},
	}
	if errs := rules.GoModPatchBumpConsistency(); len(errs) == 0 {
		t.Fatal("expected error when go_mod_patch_bump globs are missing from tiers.high")
	}
	rules.Tiers["high"] = append(rules.Tiers["high"], "go.mod", "**/go.mod")
	if errs := rules.GoModPatchBumpConsistency(); len(errs) != 0 {
		t.Fatalf("unexpected: %v", errs)
	}
}
