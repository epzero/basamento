package ir

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/epzero/basamento/flow"
)

// scalarFlow is a flow of two nodes whose only expression is the condition on
// the second edge, so a test can put one expression at a size. The expression
// is not on the edge from START, which takes none (REQ-FLOW-066).
func scalarFlow(when string) *flow.IR {
	return &flow.IR{
		Name:    "sized",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes:   taskNodes("check", "done"),
		Edges: []flow.Edge{
			{From: Start, To: "check"},
			{From: "check", To: "done", When: when},
			{From: "done", To: Fail},
		},
	}
}

// celExpression returns a CEL expression of exactly n bytes that type-checks:
// "true" joined by " || true", padded with trailing spaces. The compiler's size
// limit is a property of the bytes, not of the parse tree.
func celExpression(n int) string {
	const unit = " || true"
	s := "true"
	for len(s)+len(unit) <= n {
		s += unit
	}
	return s + strings.Repeat(" ", n-len(s))
}

func TestValidateAcceptsAnExpressionAtItsMaximum(t *testing.T) {
	f := scalarFlow(celExpression(MaxExpressionBytes))
	if err := Validate(f); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsAnExpressionAboveItsMaximum(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*flow.IR)
		want   string
	}{
		{
			name:   "edge condition",
			mutate: func(f *flow.IR) { f.Edges[1].When = celExpression(MaxExpressionBytes + 1) },
			want:   `edge "check" -> "done" when`,
		},
		{
			name: "node input mapping",
			mutate: func(f *flow.IR) {
				node := f.Nodes["check"]
				node.In = celExpression(MaxExpressionBytes + 1)
				f.Nodes["check"] = node
			},
			want: `node "check" input mapping`,
		},
		{
			name: "guard condition",
			mutate: func(f *flow.IR) {
				node := f.Nodes["check"]
				node.Guard = &flow.Guard{While: celExpression(MaxExpressionBytes + 1)}
				f.Nodes["check"] = node
			},
			want: `node "check" guard while`,
		},
		{
			name: "gate accept_if",
			mutate: func(f *flow.IR) {
				node := f.Nodes["check"]
				node.Kind = "gate"
				node.Spec = json.RawMessage(fmt.Sprintf(`{"accept_if":%q}`, celExpression(MaxExpressionBytes+1)))
				f.Nodes["check"] = node
			},
			want: `node "check" gate accept_if`,
		},
		{
			name: "map over",
			mutate: func(f *flow.IR) {
				node := f.Nodes["check"]
				node.Kind = "map"
				node.Spec = json.RawMessage(fmt.Sprintf(`{"over":%q}`, celExpression(MaxExpressionBytes+1)))
				f.Nodes["check"] = node
			},
			want: `node "check" map over`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := scalarFlow("")
			test.mutate(f)
			requireInvalid(t, Validate(f),
				"expression size limit of 16384 bytes exceeded",
				test.want,
			)
		})
	}
}

func TestValidateRejectsAnExpressionAboveTheLimitBeforeAnyGraphWork(t *testing.T) {
	// The oversized expression sits on a node that also makes the flow
	// invalid. The size limit is checked first, so its message wins.
	f := scalarFlow(celExpression(MaxExpressionBytes + 1))
	f.Nodes["orphan"] = flow.Node{Kind: "task"}
	requireInvalid(t, Validate(f), "expression size limit")
}

func TestValidateRejectsTooManyNodes(t *testing.T) {
	f := longChain(MaxNodes + 1)
	requireInvalid(t, Validate(f), "node count limit of 4096 nodes exceeded", "4097 nodes")
}

func TestValidateRejectsTooManyEdges(t *testing.T) {
	f := longChain(MaxNodes)
	padEdges(f, MaxEdges+1)
	requireInvalid(t, Validate(f), "edge count limit of 16384 edges exceeded", "16385 edges")
}

func TestValidateAcceptsNodeAndEdgeCountsAtTheirMaxima(t *testing.T) {
	f := longChain(MaxNodes)
	padEdges(f, MaxEdges)
	if err := Validate(f); err != nil {
		t.Fatal(err)
	}
	if len(f.Edges) != MaxEdges {
		t.Fatalf("built %d edges, want %d", len(f.Edges), MaxEdges)
	}
}

// padEdges adds edges to FAIL, cycling through the nodes in id order, until the
// flow carries total edges. Routing order is preserved and the graph stays
// acyclic, so the flow differs from a valid one only in its edge count.
func padEdges(f *flow.IR, total int) {
	ids := sortedKeys(f.Nodes)
	for i := 0; len(f.Edges) < total; i++ {
		f.Edges = append(f.Edges, flow.Edge{From: ids[i%len(ids)], To: Fail})
	}
}

func TestValidateAcceptsGuardBudgetsAtTheirMaxima(t *testing.T) {
	tests := []*flow.Guard{
		{MaxVisits: MaxGuardVisits},
		{CostMpts: MaxGuardCostMpts},
		{MaxVisits: MaxGuardVisits, CostMpts: MaxGuardCostMpts},
	}
	for _, guard := range tests {
		if err := Validate(guardedCycle(guard)); err != nil {
			t.Fatalf("guard %+v: %v", guard, err)
		}
	}
}

func TestValidateRejectsGuardBudgetsOutsideTheirRange(t *testing.T) {
	tests := []struct {
		name    string
		guard   *flow.Guard
		field   string
		maximum string
	}{
		{name: "negative visits", guard: &flow.Guard{MaxVisits: -1}, field: "max_visits", maximum: "1000000"},
		{name: "negative cost", guard: &flow.Guard{CostMpts: -1}, field: "cost_mpts", maximum: "1000000000"},
		{name: "visits above the maximum", guard: &flow.Guard{MaxVisits: MaxGuardVisits + 1}, field: "max_visits", maximum: "1000000"},
		{name: "cost above the maximum", guard: &flow.Guard{CostMpts: MaxGuardCostMpts + 1}, field: "cost_mpts", maximum: "1000000000"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireInvalid(t, Validate(guardedCycle(test.guard)), `node "repair"`, test.field, test.maximum)
		})
	}
}

// TestValidateRejectsABadGuardOffACycle keeps REQ-FLOW-045 a property of the
// guard, not of the cycle: a budget outside its range is rejected wherever the
// guard appears.
func TestValidateRejectsABadGuardOffACycle(t *testing.T) {
	f := simpleFlow()
	node := f.Nodes["plan"]
	node.Guard = &flow.Guard{MaxVisits: -1}
	f.Nodes["plan"] = node
	requireInvalid(t, Validate(f), `node "plan"`, "max_visits", "negative")
}

func TestCheckCanonicalSizeAcceptsTheMaximum(t *testing.T) {
	if err := CheckCanonicalSize(make([]byte, MaxDefinitionBytes)); err != nil {
		t.Fatal(err)
	}
}

func TestCheckCanonicalSizeRejectsOneByteMore(t *testing.T) {
	requireInvalid(t, CheckCanonicalSize(make([]byte, MaxDefinitionBytes+1)),
		"definition size limit of 1048576 bytes exceeded", "1048577 bytes")
}
