package expr

import (
	"math"
	"testing"

	"cel.dev/cel-go/common/types"
)

func TestMixedIntDoubleMapSize(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`{1: 'a', 2.0: 'b'}.size()`, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(nil)
	if err != nil {
		t.Fatal(err)
	}
	if res != int64(2) {
		t.Fatalf("size %v want 2", res)
	}
}

func TestMixedIntDoubleMapKeys(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`{1: 1, 2.0: 2}.map(k, k)`, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(nil)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := res.([]any)
	if !ok || len(got) != 2 {
		t.Fatalf("got %v", res)
	}
	if got[0] != int64(1) {
		t.Fatalf("first key %v want int 1", got[0])
	}
	if f, ok := got[1].(float64); !ok || f != 2.0 {
		t.Fatalf("second key %v want 2.0", got[1])
	}
}

func TestAllDoubleMapKeyOrder(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	src := `{2.0: 'b', 1.0: 'a'}.map(k, k)`
	p, err := env.Compile(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(nil)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := res.([]any)
	if !ok || len(got) != 2 {
		t.Fatalf("got %v", res)
	}
	if got[0] != float64(1.0) || got[1] != float64(2.0) {
		t.Fatalf("got %v", got)
	}
}

func TestMapKeyDoubleLessZeroAndNaN(t *testing.T) {
	if !mapKeyDoubleLess(math.Copysign(0, -1), 0) {
		t.Fatal("want −0 before +0")
	}
	nan := math.NaN()
	if mapKeyDoubleLess(nan, 1.0) {
		t.Fatal("NaN after finite")
	}
	if !mapKeyDoubleLess(1.0, nan) {
		t.Fatal("finite before NaN")
	}
}

func TestSortedAnyMapNaNKeyOrder(t *testing.T) {
	adapter := newSortedAdapter(nil)
	entries := []sortedEntry{
		{key: mapKey{kind: keyDouble, d: math.NaN(), key: types.Double(math.NaN())}, val: types.Int(1)},
		{key: mapKey{kind: keyDouble, d: 1.0, key: types.Double(1)}, val: types.Int(2)},
	}
	m := newSortedAnyMap(adapter, entries)
	it := m.Iterator()
	var keys []float64
	for it.HasNext().Equal(types.Bool(true)) == types.True {
		k := it.Next().(types.Double)
		keys = append(keys, float64(k))
	}
	if len(keys) != 2 || keys[0] != 1.0 || !math.IsNaN(keys[1]) {
		t.Fatalf("keys %v", keys)
	}
}

func TestAllDoubleMapStable200Evaluations(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	src := `{3.0: 3, 1.0: 1, 2.0: 2}.map(k, k)`
	p, err := env.Compile(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	var first []any
	for i := 0; i < 200; i++ {
		res, err := p.Eval(nil)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := res.([]any)
		if !ok {
			t.Fatalf("got %T", res)
		}
		if first == nil {
			first = got
		} else {
			for j := range first {
				if got[j] != first[j] {
					t.Fatalf("iter %d index %d got %v want %v", i, j, got[j], first[j])
				}
			}
		}
	}
}

func TestMapKeyOrderStable200Evaluations(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	src := `{true: 1, 42: 2, 2.5: 3, 'b': 4, 'a': 5}.map(k, k)`
	p, err := env.Compile(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []any{true, int64(42), 2.5, "a", "b"}
	var first []any
	for i := 0; i < 200; i++ {
		res, err := p.Eval(nil)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := res.([]any)
		if !ok {
			t.Fatalf("got %T", res)
		}
		if i == 0 {
			if len(got) != len(want) {
				t.Fatalf("got %v", got)
			}
			for j := range want {
				if got[j] != want[j] {
					t.Fatalf("index %d got %v want %v", j, got[j], want[j])
				}
			}
		}
		if first == nil {
			first = got
		} else if len(got) != len(first) {
			t.Fatalf("iter %d length %d want %d", i, len(got), len(first))
		} else {
			for j := range first {
				if got[j] != first[j] {
					t.Fatalf("iter %d index %d got %v want %v", i, j, got[j], first[j])
				}
			}
		}
	}
}
