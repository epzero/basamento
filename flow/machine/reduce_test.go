package machine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
)

func reducerMachine(t *testing.T, reducers map[string]string) *Machine {
	t.Helper()
	properties := map[string]any{}
	for key := range reducers {
		properties[key] = map[string]any{}
	}
	schema, err := json.Marshal(map[string]any{"type": "object", "properties": properties})
	if err != nil {
		t.Fatal(err)
	}
	return requireMachine(t, &flow.IR{
		Name:    "reducers",
		Version: 1,
		State: flow.StateSpec{
			Schema:   schema,
			Reducers: reducers,
		},
		Nodes: map[string]flow.Node{"a": {Kind: "task"}},
		Edges: []flow.Edge{{From: "START", To: "a"}},
	})
}

func TestReducers(t *testing.T) {
	t.Run("replace", func(t *testing.T) {
		m := reducerMachine(t, nil)
		state := map[string]any{"key": "old"}
		if err := m.reduceKey(state, "key", "new"); err != nil {
			t.Fatal(err)
		}
		if state["key"] != "new" {
			t.Fatalf("key = %v, want new", state["key"])
		}
	})

	t.Run("append scalar", func(t *testing.T) {
		m := reducerMachine(t, map[string]string{"log": "append"})
		state := map[string]any{"log": []any{"a"}}
		if err := m.reduceKey(state, "log", "b"); err != nil {
			t.Fatal(err)
		}
		assertJSON(t, state["log"], `["a","b"]`)
	})

	t.Run("append array", func(t *testing.T) {
		m := reducerMachine(t, map[string]string{"log": "append"})
		state := map[string]any{"log": []any{"a"}}
		if err := m.reduceKey(state, "log", []any{"b", "c"}); err != nil {
			t.Fatal(err)
		}
		assertJSON(t, state["log"], `["a","b","c"]`)
	})

	t.Run("merge", func(t *testing.T) {
		m := reducerMachine(t, map[string]string{"doc": "merge"})
		state := map[string]any{"doc": map[string]any{"a": 1, "b": 2}}
		if err := m.reduceKey(state, "doc", map[string]any{"b": 3, "c": 4}); err != nil {
			t.Fatal(err)
		}
		assertJSON(t, state["doc"], `{"a":1,"b":3,"c":4}`)
	})

	t.Run("sum integers", func(t *testing.T) {
		m := reducerMachine(t, map[string]string{"n": "sum"})
		state := map[string]any{"n": json.Number("2")}
		if err := m.reduceKey(state, "n", json.Number("3")); err != nil {
			t.Fatal(err)
		}
		if got := state["n"].(json.Number).String(); got != "5" {
			t.Fatalf("n = %s, want 5", got)
		}
	})

	t.Run("sum numbers", func(t *testing.T) {
		m := reducerMachine(t, map[string]string{"n": "sum"})
		state := map[string]any{"n": json.Number("1.5")}
		if err := m.reduceKey(state, "n", json.Number("2")); err != nil {
			t.Fatal(err)
		}
		if got := state["n"].(json.Number).String(); got != "3.5" {
			t.Fatalf("n = %s, want 3.5", got)
		}
	})

	t.Run("merge_by keeps the first position and the later value", func(t *testing.T) {
		m := reducerMachine(t, map[string]string{"files": "merge_by(path)"})
		state := map[string]any{"files": []any{
			map[string]any{"path": "b", "v": 1},
			map[string]any{"path": "a", "v": 2},
		}}
		if err := m.reduceKey(state, "files", []any{
			map[string]any{"path": "c", "v": 3},
			map[string]any{"path": "a", "v": 4},
		}); err != nil {
			t.Fatal(err)
		}
		assertJSON(t, state["files"], `[{"path":"b","v":1},{"path":"a","v":4},{"path":"c","v":3}]`)
	})

	t.Run("append rejects a non-array current value", func(t *testing.T) {
		m := reducerMachine(t, map[string]string{"log": "append"})
		err := m.reduceKey(map[string]any{"log": "x"}, "log", "y")
		if core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("error %v, want invalid_argument", err)
		}
	})

	t.Run("merge_by rejects an entry without the key", func(t *testing.T) {
		m := reducerMachine(t, map[string]string{"files": "merge_by(path)"})
		err := m.reduceKey(map[string]any{}, "files", []any{map[string]any{"name": "x"}})
		if core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("error %v, want invalid_argument", err)
		}
	})
}

func TestSumOverflowRejectsPositiveAndNegative(t *testing.T) {
	m := reducerMachine(t, map[string]string{"n": "sum"})
	for _, tc := range []struct {
		name  string
		start string
		add   string
	}{
		{"max plus one", "9223372036854775807", "1"},
		{"min minus one", "-9223372036854775808", "-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := map[string]any{"n": json.Number(tc.start)}
			err := m.reduceKey(state, "n", json.Number(tc.add))
			if core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("error %v, want invalid_argument", err)
			}
			if !strings.Contains(err.Error(), "overflow") {
				t.Fatalf("error %q, want overflow in message", err)
			}
			if !strings.Contains(err.Error(), `"n"`) {
				t.Fatalf("error %q, want key name", err)
			}
		})
	}
}

func TestSumAtMaxInt64StillWorks(t *testing.T) {
	m := reducerMachine(t, map[string]string{"n": "sum"})
	state := map[string]any{"n": json.Number("9223372036854775806")}
	if err := m.reduceKey(state, "n", json.Number("1")); err != nil {
		t.Fatal(err)
	}
	if got := state["n"].(json.Number).String(); got != "9223372036854775807" {
		t.Fatalf("n = %s, want MaxInt64", got)
	}
}

func assertJSON(t *testing.T, value any, want string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != want {
		t.Fatalf("value %s, want %s", encoded, want)
	}
}
