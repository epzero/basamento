package expr

import "testing"

func TestBannedCorpusRejects(t *testing.T) {
	corpus := []string{
		`def outer: def inner: now; inner; outer`,
		`def outer: (def inner: now; inner); outer`,
		`{t: (def x: now; x)}`,
		`"\(now)"`,
		`reduce range(1) as $i (0; (def x: now; x))`,
		`foreach range(1) as $i (0; .; (def x: now; x))`,
		`foreach range(1) as $i (0; .; .; (def x: now; x))`,
		`def f(g): g; f(now)`,
		`. as {(now): $x} | $x`,
		`try now catch 0 ?// now`,
		`label $out | (def x: now; x)`,
		`break now`,
		`[now].[] | now`,
		`if (def x: now; x) then 0 else 1 end`,
		`try (def x: now; x) catch 0`,
		`(def outer: now; outer) | 0`,
		`def a: def b: def c: now; c; b; a`,
		`{k: env}`,
		`. | halt`,
		`. | input_filename`,
		`modulemeta`,
		`get_search_list`,
		`input_line_number`,
		`def in_obj: {x: now}; in_obj`,
		`def in_reduce: reduce .[] as $x (0; now); in_reduce`,
		`def in_foreach: foreach .[] as $x (0; now; now); in_foreach`,
		`def pass_now($f): $f; pass_now(now)`,
		`.. | now`,
		`recurse(now)`,
		`(now, now)`,
		`select(now > 0)`,
		`map(now)`,
		`{($ENV): 1}`,
		`include "x"`,
		`import "x" as y`,
		`.[now]`,
		`.[env]`,
		`+$now`,
		`-(now)`,
		`not now`,
		`paths(now)`,
	}
	for _, src := range corpus {
		t.Run(src, func(t *testing.T) {
			_, err := CompileMapping(src, nil)
			if err == nil {
				t.Fatal("want compile error")
			}
		})
	}
}
