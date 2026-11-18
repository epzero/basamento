package steps

import (
	"strings"
	"testing"

	"github.com/epzero/basamento/core/schema"
)

func TestCompareViolationRunsRequiresThreeRuns(t *testing.T) {
	want := []schema.Violation{{Location: "/b", Keyword: "type"}}
	other := []schema.Violation{{Location: "/a", Keyword: "type"}}
	for _, tc := range []struct {
		name string
		runs [][]schema.Violation
		ok   bool
	}{
		{"one", [][]schema.Violation{want}, false},
		{"two", [][]schema.Violation{want, want}, false},
		{"four", [][]schema.Violation{want, want, want, want}, false},
		{"three_match", [][]schema.Violation{want, want, want}, true},
		{"third_differs", [][]schema.Violation{want, want, other}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := compareViolationRuns(tc.runs, want)
			if tc.ok && err != nil {
				t.Fatal(err)
			}
			if !tc.ok && (err == nil || !strings.Contains(err.Error(), "expected 3 validation runs") && !strings.Contains(err.Error(), "run")) {
				t.Fatalf("want error, got %v", err)
			}
		})
	}
}
