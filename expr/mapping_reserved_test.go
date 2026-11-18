package expr

import (
	"encoding/json"
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

func TestCompileMappingAcceptsBannedNameDataFields(t *testing.T) {
	data := map[string]any{
		"input":          float64(5),
		"env":            map[string]any{"HOME": "/x"},
		"debug":          true,
		"now":            "2020-01-01T00:00:00Z",
		"halt":           false,
		"stderr":         "err",
		"inputs":         []any{float64(1), float64(2)},
		"input_filename": "stdin",
		"a":              map[string]any{"input": map[string]any{"b": float64(9)}},
	}
	cases := []struct {
		src   string
		input any
		want  any
	}{
		{`.input`, data, float64(5)},
		{`.env`, data, data["env"]},
		{`.debug`, data, true},
		{`.now`, data, data["now"]},
		{`.halt`, data, false},
		{`.stderr`, data, "err"},
		{`.inputs`, data, data["inputs"]},
		{`.input_filename`, data, "stdin"},
		{`{input}`, data, map[string]any{"input": float64(5)}},
		{`{env}`, data, map[string]any{"env": data["env"]}},
		{`{now}`, data, map[string]any{"now": data["now"]}},
		{`{env: .env}`, data, map[string]any{"env": data["env"]}},
		{`{"now": .now}`, data, map[string]any{"now": data["now"]}},
		{`.a.input.b`, data, float64(9)},
		{`.["env"]`, data, data["env"]},
		{`has("now")`, data, true},
		{`to_entries | map(.key)`, map[string]any{"now": 1, "env": 2, "input": 3}, []any{"env", "input", "now"}},
		{`with_entries(select(.key == "env"))`, data, map[string]any{"env": data["env"]}},
		{`. as {now: $n} | $n`, data, data["now"]},
		{`"now"`, nil, "now"},
		{`"\(.env.HOME)"`, data, "/x"},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			m, err := CompileMapping(tc.src, nil)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			got, err := m.Eval(tc.input, nil)
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			gotJ, _ := json.Marshal(got)
			wantJ, _ := json.Marshal(tc.want)
			if string(gotJ) != string(wantJ) {
				t.Fatalf("got %s want %s", gotJ, wantJ)
			}
		})
	}
}
