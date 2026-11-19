package steps

import (
	"reflect"
	"testing"
)

func TestSplitClausesKeepsQuotedAndTogether(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "path and edge chain",
			text: `START -> "plan" and "plan" -> FAIL`,
			want: []string{`START -> "plan"`, `"plan" -> FAIL`},
		},
		{
			name: "and inside a condition",
			text: `"verify" -> "review" when "state.a > 0 and state.b > 0" and "plan" -> FAIL`,
			want: []string{
				`"verify" -> "review" when "state.a > 0 and state.b > 0"`,
				`"plan" -> FAIL`,
			},
		},
		{
			name: "comma inside a condition",
			text: `"verify" -> "review" when "state.a in [1, 2] and state.b > 0"`,
			want: []string{`"verify" -> "review" when "state.a in [1, 2] and state.b > 0"`},
		},
		{
			name: "and and comma inside a single-quoted literal",
			text: `"verify" -> "review" when "state.name == 'a, b and c'"`,
			want: []string{`"verify" -> "review" when "state.name == 'a, b and c'"`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := splitClauses(test.text); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("clauses %q, want %q", got, test.want)
			}
		})
	}
}

func TestParseDeclaredEdgesKeepsConditionWithAnd(t *testing.T) {
	edges, err := parseDeclaredEdges(
		`"verify" -> "review" when "state.a > 0 and state.b > 0", "plan" -> FAIL without a condition`)
	if err != nil {
		t.Fatal(err)
	}
	want := []declaredEdge{
		{from: "verify", to: "review", when: "state.a > 0 and state.b > 0"},
		{from: "plan", to: "FAIL"},
	}
	if !reflect.DeepEqual(edges, want) {
		t.Fatalf("edges %+v, want %+v", edges, want)
	}
}
