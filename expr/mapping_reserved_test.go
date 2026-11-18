package expr

import (
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestCompileMappingRejectsReservedDefAndCall(t *testing.T) {
	cases := []struct {
		src  string
		name string
	}{
		{`def now: 1; now`, "now"},
		{`def env: 1; env`, "env"},
		{`def f(now): now; f(1)`, "now"},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			_, err := CompileMapping(tc.src, nil)
			if err == nil {
				t.Fatal("want compile error")
			}
			if core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("code %s", core.CodeOf(err))
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.name) {
				t.Fatalf("message %q missing %q", msg, tc.name)
			}
			if !strings.Contains(msg, "reserved name") {
				t.Fatalf("message %q missing reserved name detail", msg)
			}
		})
	}
}

func TestCompileMappingAcceptsNowOutsideCalls(t *testing.T) {
	accept := []string{`{"now": 1}`, `"now"`}
	for _, src := range accept {
		t.Run(src, func(t *testing.T) {
			if _, err := CompileMapping(src, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCompileMappingRejectsNowFieldShorthandToday(t *testing.T) {
	// Fail-closed scan treats object-key shorthand for clock/env builtins as reserved.
	reject := []string{`{now}`, `.now`}
	for _, src := range reject {
		t.Run(src, func(t *testing.T) {
			_, err := CompileMapping(src, nil)
			if err == nil {
				t.Fatal("want compile error")
			}
		})
	}
}