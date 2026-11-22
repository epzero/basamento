package steps

import (
	"errors"
	"testing"

	"go.temporal.io/api/serviceerror"
)

func TestTemporalStub_parseQuery_accepted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		q    string
		flow bool
	}{
		{"runtime_base", `WorkflowId STARTS_WITH "acme/" AND ExecutionStatus != "ContinuedAsNew"`, false},
		{"runtime_flow", `WorkflowId STARTS_WITH "acme/" AND ExecutionStatus != "ContinuedAsNew" AND BasamentoFlow = "pay"`, true},
		{"runtime_ne_and_eq", `WorkflowId STARTS_WITH "acme/" AND ExecutionStatus != "ContinuedAsNew" AND ExecutionStatus = "Running"`, false},
		{"runtime_ne_and_in", `WorkflowId STARTS_WITH "acme/" AND ExecutionStatus != "ContinuedAsNew" AND ExecutionStatus IN ("Failed", "TimedOut")`, false},
		{"quoted_and", `WorkflowId STARTS_WITH "has AND inside"`, false},
		{"quoted_order", `WorkflowId STARTS_WITH "ORDER BY noop"`, false},
		{"quoted_or", `WorkflowId STARTS_WITH "OR else"`, false},
		{"quoted_parens", `WorkflowId STARTS_WITH "((x))"`, false},
		{"quoted_comma", `WorkflowId STARTS_WITH "a,b"`, false},
		{"in_one", `WorkflowId STARTS_WITH "x" AND ExecutionStatus IN ("Running")`, false},
		{"in_three", `WorkflowId STARTS_WITH "x" AND ExecutionStatus IN ("Running", "Failed", "TimedOut")`, false},
		{"unicode_flow", `WorkflowId STARTS_WITH "t/" AND BasamentoFlow = "支払い"`, true},
		{"empty_flow_value", `WorkflowId STARTS_WITH "t/" AND BasamentoFlow = ""`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseTemporalListQuery(tc.q, tc.flow); err != nil {
				t.Fatalf("parse: %v", err)
			}
		})
	}
}

func TestTemporalStub_parseQuery_refused(t *testing.T) {
	t.Parallel()
	type caseT struct {
		name    string
		q       string
		wantMsg string
	}
	cases := []caseT{
		{"no_prefix", `ExecutionStatus = "Running"`, ""},
		{"empty_prefix", `WorkflowId STARTS_WITH ""`, ""},
		{"or_clause", `WorkflowId STARTS_WITH "x" OR ExecutionStatus = "Running"`, ""},
		{"order_by", `WorkflowId STARTS_WITH "x" ORDER BY StartTime`, "operation is not supported: 'ORDER BY' clause"},
		{"paren_outside_in", `WorkflowId STARTS_WITH "x" AND (ExecutionStatus = "Running")`, ""},
		{"tab", "WorkflowId\tSTARTS_WITH \"x\"", ""},
		{"newline", "WorkflowId\nSTARTS_WITH \"x\"", ""},
		{"single_quote", "WorkflowId STARTS_WITH 'x'", ""},
		{"backtick", "WorkflowId STARTS_WITH `x`", ""},
		{"angle", "WorkflowId STARTS_WITH <x>", ""},
		{"missing_value_eq", `WorkflowId STARTS_WITH "x" AND ExecutionStatus =`, ""},
		{"in_no_paren", `WorkflowId STARTS_WITH "x" AND ExecutionStatus IN "Running"`, ""},
		{"in_no_comma", `WorkflowId STARTS_WITH "x" AND ExecutionStatus IN ("Running" "Failed")`, ""},
		{"in_trailing_comma", `WorkflowId STARTS_WITH "x" AND ExecutionStatus IN ("Running",)`, ""},
		{"in_empty", `WorkflowId STARTS_WITH "x" AND ExecutionStatus IN ()`, ""},
		{"in_text_after", `WorkflowId STARTS_WITH "x" AND ExecutionStatus IN ("Running") extra`, ""},
		{"unknown_attr", `WorkflowId STARTS_WITH "x" AND Unknown = "y"`, ""},
		{"dup_eq", `WorkflowId STARTS_WITH "x" AND ExecutionStatus = "Running" AND ExecutionStatus = "Failed"`, ""},
		{"dup_ne", `WorkflowId STARTS_WITH "x" AND ExecutionStatus != "A" AND ExecutionStatus != "B"`, ""},
		{"dup_in", `WorkflowId STARTS_WITH "x" AND ExecutionStatus IN ("A") AND ExecutionStatus IN ("B")`, ""},
		{"dup_prefix", `WorkflowId STARTS_WITH "a" AND WorkflowId STARTS_WITH "b"`, ""},
		{"lowercase_and", `WorkflowId STARTS_WITH "x" and ExecutionStatus = "Running"`, ""},
		{"empty_query", "", ""},
		{"only_and", `AND ExecutionStatus = "Running"`, ""},
		{"clause_trailing_and", `WorkflowId STARTS_WITH "x" AND`, ""},
		{"unterminated", `WorkflowId STARTS_WITH "oops`, ""},
		{"backslash_in_string", `WorkflowId STARTS_WITH "a\\b"`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseTemporalListQuery(tc.q, true)
			if err == nil {
				t.Fatal("expected error")
			}
			var ia *serviceerror.InvalidArgument
			if !errors.As(err, &ia) {
				t.Fatalf("type %T", err)
			}
			if tc.wantMsg != "" && err.Error() != tc.wantMsg {
				t.Fatalf("msg %q want %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

func TestTemporalStub_parseQuery_basamento_unregistered(t *testing.T) {
	_, err := parseTemporalListQuery(`WorkflowId STARTS_WITH "x" AND BasamentoFlow = "f"`, false)
	if err == nil {
		t.Fatal("expected error")
	}
	var ia *serviceerror.InvalidArgument
	if !errors.As(err, &ia) {
		t.Fatalf("type %T", err)
	}
}
