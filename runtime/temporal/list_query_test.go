package temporal

import (
	"context"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestListQueryStrings(t *testing.T) {
	tenant := core.TenantID("acme")
	base := `WorkflowId STARTS_WITH "acme/" AND ExecutionStatus != "ContinuedAsNew"`
	cases := []struct {
		flow, state, want string
		bad               bool
	}{
		{"", "", base, false},
		{"pay", "", base + ` AND BasamentoFlow = "pay"`, false},
		{"pay", "running", base + ` AND BasamentoFlow = "pay" AND ExecutionStatus = "Running"`, false},
		{"pay", "failed", base + ` AND BasamentoFlow = "pay" AND ExecutionStatus IN ("Failed", "TimedOut")`, false},
		{"", "completed", base + ` AND ExecutionStatus = "Completed"`, false},
		{"pay", "cancelled", base + ` AND BasamentoFlow = "pay" AND ExecutionStatus = "Canceled"`, false},
		{"pay", "terminated", base + ` AND BasamentoFlow = "pay" AND ExecutionStatus = "Terminated"`, false},
		{"a\"b", "", "", true},
		{"", "bogus", "", true},
	}
	for _, tc := range cases {
		q, err := listQuery(tenant, tc.flow, tc.state)
		if tc.bad {
			if err == nil {
				t.Fatalf("%q/%q should fail", tc.flow, tc.state)
			}
			continue
		}
		if err != nil || q != tc.want {
			t.Fatalf("%q/%q got %q err %v", tc.flow, tc.state, q, err)
		}
	}
	hostile := []string{
		`" OR WorkflowId STARTS_WITH "`,
		`a\b`,
		"\\\n",
		"\u201cquote",
	}
	for _, h := range hostile {
		_, err := listQuery(tenant, h, "")
		if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("hostile listQuery %q", h)
		}
		if strings.Contains(err.Error(), h) {
			t.Fatalf("echoed value")
		}
		rt, fc, dials := controlRuntimeWithDial(t)
		ctx := core.WithTenant(context.Background(), "acme")
		_, _, err = rt.List(ctx, runtime.RunQuery{Flow: h})
		if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("hostile List %q", h)
		}
		if len(fc.Calls()) != 0 || dials.Load() != 0 {
			t.Fatalf("list called client for hostile flow")
		}
	}
}
