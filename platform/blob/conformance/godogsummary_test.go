package conformance

import "testing"

func TestParseGodogSummaryZeroScenariosFails(t *testing.T) {
	t.Parallel()
	_, _, err := ParseGodogSummary("0 scenarios (0 passed)\n0 steps (0 passed)")
	if err == nil {
		t.Fatal("want error distinguishing zero selection")
	}
}

func TestParseGodogSummaryOK(t *testing.T) {
	t.Parallel()
	sc, pa, err := ParseGodogSummary("39 scenarios (39 passed)\n120 steps (120 passed)\n")
	if err != nil {
		t.Fatal(err)
	}
	if sc != 39 || pa != 39 {
		t.Fatalf("got %d/%d", sc, pa)
	}
}
