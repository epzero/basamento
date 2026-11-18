package steps

import (
	"strings"
	"testing"

	"github.com/epzero/basamento/core/schema"
)

func TestCompareViolationRunsRequiresThreeRuns(t *testing.T) {
	want := []schema.Violation{{Location: "/b", Keyword: "type"}}
	err := compareViolationRuns([][]schema.Violation{want}, want)
	if err == nil || !strings.Contains(err.Error(), "expected 3 validation runs") {
		t.Fatalf("single run should fail: %v", err)
	}
	err = compareViolationRuns([][]schema.Violation{want, want, want}, want)
	if err != nil {
		t.Fatal(err)
	}
}
