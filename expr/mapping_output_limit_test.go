package expr

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func mustMapping(t *testing.T, src string, opts ...MappingOption) *Mapping {
	t.Helper()
	m, err := CompileMapping(src, nil, opts...)
	if err != nil {
		t.Fatalf("compile %q: %v", src, err)
	}
	return m
}

func evalMapping(t *testing.T, m *Mapping, input any) (any, error) {
	t.Helper()
	return m.Eval(input, nil)
}

func TestMappingOutputLimitDefaultStopsOverLimit(t *testing.T) {
	m := mustMapping(t, `"x" * 1048575`)
	_, err := evalMapping(t, m, nil)
	if err == nil {
		t.Fatal("want error")
	}
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %s", core.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "output over the output limit") || !strings.Contains(err.Error(), "1048576") {
		t.Fatalf("message %q", err.Error())
	}
}

func TestMappingOutputLimitDefaultAllowsExactLimit(t *testing.T) {
	m := mustMapping(t, `"x" * 1048574`)
	res, err := evalMapping(t, m, nil)
	if err != nil {
		t.Fatalf("evaluation failed: %v", err)
	}
	if s, ok := res.(string); !ok || len(s) != 1048574 {
		t.Fatalf("result %T of length %d", res, len(s))
	}
}

func TestMappingOutputLimitOption(t *testing.T) {
	over := mustMapping(t, `"x" * 9`, WithOutputLimit(10))
	if _, err := evalMapping(t, over, nil); err == nil {
		t.Fatal("want error")
	} else if !strings.Contains(err.Error(), "10") {
		t.Fatalf("message %q", err.Error())
	}
	fits := mustMapping(t, `"x" * 9`, WithOutputLimit(11))
	if res, err := evalMapping(t, fits, nil); err != nil || res != "xxxxxxxxx" {
		t.Fatalf("result %v err %v", res, err)
	}
}

func TestMappingOutputLimitRejectsReservedNames(t *testing.T) {
	for _, src := range []string{
		`_add(1; 2)`,
		`_subtract(1; 2)`,
		`_multiply(2; 3)`,
		`def _add: 1; _add`,
		`def _outadd: 1; .`,
		`_outadd(.; .)`,
		`def f(_outjoin): 1; .`,
	} {
		if _, err := CompileMapping(src, nil); err == nil {
			t.Fatalf("compiled %q", src)
		} else if core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("%q: code %s", src, core.CodeOf(err))
		}
	}
}

func TestMappingOutputLimitUpdateAssignments(t *testing.T) {
	m := mustMapping(t, `.[0] += "x"`)
	if res, err := evalMapping(t, m, []any{"a"}); err != nil {
		t.Fatalf("evaluation failed: %v", err)
	} else if got := res.([]any)[0]; got != "ax" {
		t.Fatalf("result %v", res)
	}

	m = mustMapping(t, `{n: 5} | .n *= 3`)
	if res, err := evalMapping(t, m, nil); err != nil || res.(map[string]any)["n"] != 15 {
		t.Fatalf("result %v err %v", res, err)
	}

	m = mustMapping(t, `{n: 5} | .n -= 2`)
	if res, err := evalMapping(t, m, nil); err != nil || res.(map[string]any)["n"] != 3 {
		t.Fatalf("result %v err %v", res, err)
	}
}

func TestMappingOutputLimitUpdateAssignmentOverLimit(t *testing.T) {
	m := mustMapping(t, `{s: "x"} | .s *= 20`, WithOutputLimit(10))
	_, err := evalMapping(t, m, nil)
	if err == nil || !strings.Contains(err.Error(), "output over the output limit") {
		t.Fatalf("err %v", err)
	}
}

func TestMappingOutputLimitStringFunctions(t *testing.T) {
	tests := []struct {
		src   string
		input any
		want  any
	}{
		{`join(",")`, []any{"a", "b"}, "a,b"},
		{`join(",")`, []any{}, ""},
		{`implode`, []any{104, 105}, "hi"},
		{`@base64`, "hi", "aGk="},
		{`@base64d`, "aGk=", "hi"},
	}
	for _, tc := range tests {
		m := mustMapping(t, tc.src)
		got, err := evalMapping(t, m, tc.input)
		if err != nil {
			t.Fatalf("%s: %v", tc.src, err)
		}
		if got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.src, got, tc.want)
		}
	}
}

func TestMappingOutputLimitStringFunctionsOverLimit(t *testing.T) {
	tests := []struct {
		src   string
		input any
	}{
		{`join(",")`, []any{strings.Repeat("a", 40), strings.Repeat("b", 40)}},
		{`implode`, []any{97, 98, 99, 100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111, 112, 113, 114, 115, 116}},
		{`@base64`, strings.Repeat("a", 40)},
		{`@base64d`, "YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYQ=="},
	}
	for _, tc := range tests {
		m := mustMapping(t, tc.src, WithOutputLimit(20))
		if _, err := evalMapping(t, m, tc.input); err == nil {
			t.Fatalf("%s: want error", tc.src)
		}
	}
}

func TestMappingOutputLimitMeasuresContainers(t *testing.T) {
	m := mustMapping(t, `[range(100)]`, WithOutputLimit(50))
	if _, err := evalMapping(t, m, nil); err == nil || !strings.Contains(err.Error(), "50") {
		t.Fatalf("err %v", err)
	}
	m = mustMapping(t, `{a: ("x" * 100)}`, WithOutputLimit(50))
	if _, err := evalMapping(t, m, nil); err == nil {
		t.Fatal("want error")
	}
}

func TestMappingOutputLimitMeasuresPassedThroughValue(t *testing.T) {
	m := mustMapping(t, `.`, WithOutputLimit(20))
	if _, err := evalMapping(t, m, strings.Repeat("x", 30)); err == nil {
		t.Fatal("want error")
	}
	if res, err := evalMapping(t, m, "short"); err != nil || res != "short" {
		t.Fatalf("result %v err %v", res, err)
	}
}

func TestMappingOutputLimitLongOperatorChain(t *testing.T) {
	src := `"a" * 10` + strings.Repeat(` + "b"`, 40)
	m := mustMapping(t, src)
	res, err := evalMapping(t, m, nil)
	if err != nil {
		t.Fatalf("evaluation failed: %v", err)
	}
	if res != strings.Repeat("a", 10)+strings.Repeat("b", 40) {
		t.Fatalf("result %v", res)
	}
}

func TestMappingOutputLimitKeepsUserDefinitions(t *testing.T) {
	m := mustMapping(t, `def join(x): "custom"; join(1)`)
	res, err := evalMapping(t, m, []any{"a", "b"})
	if err != nil {
		t.Fatalf("evaluation failed: %v", err)
	}
	if res != "custom" {
		t.Fatalf("result %v", res)
	}

	m = mustMapping(t, `def f: "a" + "b"; f`)
	if res, err := evalMapping(t, m, nil); err != nil || res != "ab" {
		t.Fatalf("result %v err %v", res, err)
	}
}

func TestMappingOutputLimitInterpolationAndFormats(t *testing.T) {
	tests := []struct {
		src   string
		input any
		want  any
	}{
		{`"x\(1 + 2)"`, nil, "x3"},
		{`@base64 "x\(.)"`, "hi", "xaGk="},
		{`@base64`, "hi", "aGk="},
	}
	for _, tc := range tests {
		m := mustMapping(t, tc.src)
		res, err := evalMapping(t, m, tc.input)
		if err != nil {
			t.Fatalf("%s: %v", tc.src, err)
		}
		if res != tc.want {
			t.Fatalf("%s: got %v want %v", tc.src, res, tc.want)
		}
	}
}

func TestMappingOutputLimitPathsStayAssignable(t *testing.T) {
	tests := []struct {
		src   string
		input any
		want  any
	}{
		{`{a: 1, b: [2]} | .a = 3`, nil, nil},
		{`.a = [1, 2]`, map[string]any{}, nil},
		{`[1, 2][0]`, nil, int(1)},
		{`.a += 1`, map[string]any{"a": 1}, nil},
		{`.[0] += 1`, []any{1, 2}, nil},
	}
	for _, tc := range tests {
		m := mustMapping(t, tc.src)
		if _, err := evalMapping(t, m, tc.input); err != nil {
			t.Fatalf("%s: %v", tc.src, err)
		}
	}
	m := mustMapping(t, `{a: 1, b: [2]} | .a = 3`)
	res, err := evalMapping(t, m, nil)
	if err != nil {
		t.Fatalf("evaluation failed: %v", err)
	}
	if got := res.(map[string]any)["a"]; got != 3 {
		t.Fatalf("result %v", res)
	}
}

func TestMappingOutputLimitContainerAddSize(t *testing.T) {
	fits := mustMapping(t, `[1] + [2]`, WithOutputLimit(5))
	if res, err := evalMapping(t, fits, nil); err != nil {
		t.Fatalf("evaluation failed: %v", err)
	} else if b, _ := json.Marshal(res); string(b) != "[1,2]" {
		t.Fatalf("result %s", b)
	}
	over := mustMapping(t, `[1] + [2]`, WithOutputLimit(4))
	if _, err := evalMapping(t, over, nil); err == nil {
		t.Fatal("want error")
	}

	obj := mustMapping(t, `{a: 1} + {b: 2}`, WithOutputLimit(13))
	if res, err := evalMapping(t, obj, nil); err != nil {
		t.Fatalf("evaluation failed: %v", err)
	} else if b, _ := json.Marshal(res); string(b) != `{"a":1,"b":2}` {
		t.Fatalf("result %s", b)
	}
	objOver := mustMapping(t, `{a: 1} + {b: 2}`, WithOutputLimit(12))
	if _, err := evalMapping(t, objOver, nil); err == nil {
		t.Fatal("want error")
	}
}

func TestJSONFloatSize(t *testing.T) {
	tests := map[float64]string{
		0:     "0",
		1.5:   "1.5",
		-2.25: "-2.25",
		1e20:  "100000000000000000000",
		1e21:  "1e+21",
		1e-7:  "1e-7",
	}
	for f, want := range tests {
		if got := formatJSONFloat(f); got != want {
			t.Fatalf("%v: got %q want %q", f, got, want)
		}
	}
	if got := formatJSONFloat(math.Copysign(0, -1)); got != "0" {
		t.Fatalf("-0: got %q", got)
	}
}

func TestStringJSONSize(t *testing.T) {
	tests := map[string]int{
		"":      2,
		"abc":   5,
		"a\"b":  6,
		"a\\b":  6,
		"a\nb":  6,
		"\x01":  8,
		"é":     4,
		"😀":     6,
		"a<b&c": 7,
	}
	for s, want := range tests {
		if got := stringJSONSize(s); got != want {
			t.Fatalf("%q: got %d want %d", s, got, want)
		}
	}
}

// TestMappingOutputLimitDefinitionBeforeOperator pins that a def directly
// before an operator chain compiles instead of recursing in the rewriter
// (round 4, finding 1).
func TestMappingOutputLimitDefinitionBeforeOperator(t *testing.T) {
	tests := []struct {
		src   string
		input any
		want  any
	}{
		{`def f: 1; 2 + 3`, nil, 5},
		{`def f: 1; 2 - 3`, nil, -1},
		{`def f: 1; "a" * 2`, nil, "aa"},
		{`def f: 1; f + f`, nil, 2},
		{`def f: 1; 2 + 3 + 4`, nil, 9},
		{`def f: 1; (2 + 3)`, nil, 5},
		{`def f(x): x; 1 + 2`, nil, 3},
		{`def f: 1; .a + .b + .c`, map[string]any{"a": 1, "b": 2, "c": 3}, 6},
		{`def f: 1; .a + .b | . * 2`, map[string]any{"a": 1, "b": 2}, 6},
	}
	for _, tc := range tests {
		m := mustMapping(t, tc.src)
		got, err := evalMapping(t, m, tc.input)
		if err != nil {
			t.Fatalf("%s: %v", tc.src, err)
		}
		if got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.src, got, tc.want)
		}
	}
}

// TestMappingOutputLimitDefinitionBeforeUpdateAssignment pins the update forms
// with a def in front of them.
func TestMappingOutputLimitDefinitionBeforeUpdateAssignment(t *testing.T) {
	m := mustMapping(t, `def f: 1; .n += 2`)
	res, err := evalMapping(t, m, map[string]any{"n": 1})
	if err != nil {
		t.Fatalf("evaluation failed: %v", err)
	}
	if b, _ := json.Marshal(res); string(b) != `{"n":3}` {
		t.Fatalf("result %s", b)
	}

	m = mustMapping(t, `def f: 1; .[0] += 1`)
	res, err = evalMapping(t, m, []any{1})
	if err != nil {
		t.Fatalf("evaluation failed: %v", err)
	}
	if b, _ := json.Marshal(res); string(b) != `[2]` {
		t.Fatalf("result %s", b)
	}
}

// TestMappingOutputLimitMapAddIsShallow pins that a map + is sized as gojq's
// shallow right-wins merge, not as the deep merge of * (round 4, finding 2).
func TestMappingOutputLimitMapAddIsShallow(t *testing.T) {
	m := mustMapping(t, `. + {"a": {"b": 1}}`, WithOutputLimit(50))
	input := map[string]any{"a": map[string]any{"big": strings.Repeat("x", 40)}}
	res, err := evalMapping(t, m, input)
	if err != nil {
		t.Fatalf("shallow merge rejected: %v", err)
	}
	if b, _ := json.Marshal(res); string(b) != `{"a":{"b":1}}` {
		t.Fatalf("result %s", b)
	}

	m = mustMapping(t, `{a: {x: 1}} + {a: {y: 2}}`)
	res, err = evalMapping(t, m, nil)
	if err != nil {
		t.Fatalf("evaluation failed: %v", err)
	}
	if b, _ := json.Marshal(res); string(b) != `{"a":{"y":2}}` {
		t.Fatalf("result %s", b)
	}

	// The shallow size is exact, not merely smaller: an output over the limit
	// still fails.
	m = mustMapping(t, `{a: ("x" * 40)} + {b: 1}`, WithOutputLimit(50))
	if _, err := evalMapping(t, m, nil); err == nil {
		t.Fatal("want error")
	}
}

// TestAddedObjectSizeMatchesShallowMerge pins that the size of a map + agrees
// with the canonical size of the object gojq builds: a right-wins merge.
func TestAddedObjectSizeMatchesShallowMerge(t *testing.T) {
	cases := []struct{ a, b map[string]any }{
		{map[string]any{"a": 1}, map[string]any{"b": 2}},
		{map[string]any{"a": 1, "b": 2}, map[string]any{"b": 3}},
		{map[string]any{"a": map[string]any{"x": 1}}, map[string]any{"a": map[string]any{"y": 2}}},
		{map[string]any{"a": strings.Repeat("x", 40)}, map[string]any{"a": 1, "b": []any{1, 2}}},
		{map[string]any{}, map[string]any{"a": 1}},
		{map[string]any{"a": 1}, map[string]any{}},
		{map[string]any{}, map[string]any{}},
	}
	for _, tc := range cases {
		merged := make(map[string]any, len(tc.a)+len(tc.b))
		for k, v := range tc.a {
			merged[k] = v
		}
		for k, v := range tc.b {
			merged[k] = v
		}
		want, err := exactJSONSize(merged)
		if err != nil {
			t.Fatal(err)
		}
		got, over, err := addedObjectSize(tc.a, tc.b, -1)
		if err != nil || over {
			t.Fatalf("%v + %v: over %v err %v", tc.a, tc.b, over, err)
		}
		if got != want {
			t.Fatalf("%v + %v: size %d want %d", tc.a, tc.b, got, want)
		}
	}
}

// TestMergedObjectSizeMatchesDeepMerge pins the deep-merge size used for *.
func TestMergedObjectSizeMatchesDeepMerge(t *testing.T) {
	cases := []struct{ a, b map[string]any }{
		{map[string]any{"a": 1}, map[string]any{"b": 2}},
		{map[string]any{"a": 1, "b": 2}, map[string]any{"b": 3}},
		{map[string]any{"a": map[string]any{"x": 1}}, map[string]any{"a": map[string]any{"y": 2}}},
		{map[string]any{"a": map[string]any{"x": map[string]any{"p": 1}}}, map[string]any{"a": map[string]any{"x": map[string]any{"q": 2}}}},
		{map[string]any{}, map[string]any{}},
	}
	for _, tc := range cases {
		merged := deepMergeJSON(tc.a, tc.b)
		want, err := exactJSONSize(merged)
		if err != nil {
			t.Fatal(err)
		}
		got, over, err := mergedObjectSize(tc.a, tc.b, -1)
		if err != nil || over {
			t.Fatalf("%v * %v: over %v err %v", tc.a, tc.b, over, err)
		}
		if got != want {
			t.Fatalf("%v * %v: size %d want %d", tc.a, tc.b, got, want)
		}
	}
}

func deepMergeJSON(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		mb, bok := v.(map[string]any)
		ma, aok := out[k].(map[string]any)
		if bok && aok {
			out[k] = deepMergeJSON(ma, mb)
			continue
		}
		out[k] = v
	}
	return out
}

// TestMappingOutputLimitCountsInvalidUTF8 pins that an invalid byte counts as
// the three bytes of the U+FFFD the canonical form writes (round 4, finding 3).
func TestStringJSONSizeInvalidUTF8(t *testing.T) {
	tests := []struct {
		s    string
		want int
	}{
		{string([]byte{0xff}), 5},
		{string([]byte{0xff, 0xff, 0xff}), 11},
		{"a\xffb", 7},
		// A UTF-8 encoded surrogate is invalid UTF-8.
		{string([]byte{0xed, 0xa0, 0x80}), 11},
		// A truncated sequence has one invalid byte per byte left.
		{string([]byte{0xe2, 0x82}), 8},
	}
	for _, tc := range tests {
		if got := stringJSONSize(tc.s); got != tc.want {
			t.Fatalf("%q: got %d want %d", tc.s, got, tc.want)
		}
	}
}

func TestMappingOutputLimitCountsInvalidUTF8(t *testing.T) {
	invalid := strings.Repeat(string([]byte{0xff}), 30)
	encoded := base64.StdEncoding.EncodeToString([]byte(invalid))

	m := mustMapping(t, `@base64d`, WithOutputLimit(40))
	if _, err := evalMapping(t, m, encoded); err == nil {
		t.Fatal("@base64d output with invalid bytes passed under the limit")
	}
	// The canonical size is 30 invalid bytes × 3 + 2 quotes.
	m = mustMapping(t, `@base64d`, WithOutputLimit(92))
	if res, err := evalMapping(t, m, encoded); err != nil {
		t.Fatalf("output at the exact canonical size failed: %v", err)
	} else if res != invalid {
		t.Fatalf("result %q", res)
	}

	// Invalid UTF-8 in the input data is measured the same way.
	m = mustMapping(t, `.`, WithOutputLimit(7))
	if _, err := evalMapping(t, m, string([]byte{0xff, 0xff})); err == nil {
		t.Fatal("invalid UTF-8 input passed under the limit")
	}
	m = mustMapping(t, `.`, WithOutputLimit(8))
	if _, err := evalMapping(t, m, string([]byte{0xff, 0xff})); err != nil {
		t.Fatalf("input at the exact canonical size failed: %v", err)
	}
}

func TestMappingOutputLimitImplodeSurrogate(t *testing.T) {
	m := mustMapping(t, `implode`, WithOutputLimit(4))
	if _, err := evalMapping(t, m, []any{0xd800}); err == nil {
		t.Fatal("a surrogate passed under the limit")
	}
	m = mustMapping(t, `implode`, WithOutputLimit(5))
	if res, err := evalMapping(t, m, []any{0xd800}); err != nil || res != "\ufffd" {
		t.Fatalf("result %q err %v", res, err)
	}
}

// TestMappingNoJSONFormCause pins that a value with no JSON form is reported
// as its own cause, not as an output-limit failure (round 4, nit).
func TestMappingNoJSONFormCause(t *testing.T) {
	err := measureMappingValue(make(chan int), 100)
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "no JSON form") {
		t.Fatalf("message %q", err.Error())
	}
	if strings.Contains(err.Error(), "output limit") {
		t.Fatalf("blamed on the limit: %q", err.Error())
	}
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %s", core.CodeOf(err))
	}

	err = checkAddSize(map[string]any{"a": make(chan int)}, map[string]any{"b": 1}, 100)
	if err == nil || !strings.Contains(err.Error(), "no JSON form") {
		t.Fatalf("map +: %v", err)
	}
	err = checkMulSize(map[string]any{"a": make(chan int)}, map[string]any{"b": 1}, 100)
	if err == nil || !strings.Contains(err.Error(), "no JSON form") {
		t.Fatalf("map *: %v", err)
	}
}

// TestWithOutputLimitRejectsNonPositive pins that the option cannot silently
// disable the limit (round 4, nit).
func TestWithOutputLimitRejectsNonPositive(t *testing.T) {
	for _, limit := range []int{-1, 0} {
		_, err := CompileMapping(`.`, nil, WithOutputLimit(limit))
		if err == nil {
			t.Fatalf("WithOutputLimit(%d) accepted", limit)
		}
		if core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("WithOutputLimit(%d): code %s", limit, core.CodeOf(err))
		}
		if !strings.Contains(err.Error(), "output limit") {
			t.Fatalf("WithOutputLimit(%d): message %q", limit, err.Error())
		}
	}
	if _, err := CompileMapping(`.`, nil, WithOutputLimit(1)); err != nil {
		t.Fatalf("WithOutputLimit(1): %v", err)
	}
}

func TestMappingOutputLimitKeepsExactIntegers(t *testing.T) {
	m := mustMapping(t, `.big`)
	input := map[string]any{"big": big.NewInt(0).SetBits([]big.Word{0, 0, 1234567890})}
	res, err := evalMapping(t, m, input)
	if err != nil {
		t.Fatalf("evaluation failed: %v", err)
	}
	want := input["big"].(*big.Int).String()
	if got, ok := res.(json.Number); !ok || got.String() != want {
		t.Fatalf("result %v want %s", res, want)
	}
}
