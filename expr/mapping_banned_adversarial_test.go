package expr

import "testing"

// Adversarial jq programs for banned-builtin positions beyond the fixed corpus.
func TestAdversarialBannedProgramsRejected(t *testing.T) {
	progs := []string{
		`def a: def b: def c: now; c; b; a`,
		`(def x: now; x) as $y | $y`,
		`reduce .[] as $x (0; (def i: now; i))`,
		`foreach .[] as $x (0; .; (def i: now; i))`,
		`try (label $l | break now) catch 0`,
		`if (def n: now; n) then 1 else 2 end`,
		`[1, (def n: now; n), 2][1]`,
		`{a: (def n: now; n), b: 1}`,
		`"\(def n: now; n)"`,
		`def f($x): $x; f(now)`,
		`. as {$x: (now)} | $x`,
		`0 | . + (now)`,
		`select(def n: now; n > 0)`,
		`(now | .), (now | .)`,
		`def g: input; g`,
		`def g: inputs; [g] | length`,
		`. | (def h: stderr; h)`,
		`[(now), localtime, strflocaltime] | length`,
		`def wrap: . | now; wrap`,
		`.. | (def n: now; n)`,
		`recurse(def n: now; n))`,
		`paths(now)[0]`,
		`transpose(now)`,
		`walk(now)`,
		`def chain: (def inner: now; inner); chain`,
		`reduce range(2) as $i ((def s: now; s); .)`,
		`foreach range(2) as $i (0; (def s: now; s); .)`,
		`foreach range(2) as $i (0; .; (def s: now; s))`,
		`label $l | def m: now; m`,
		`include "x" | .`,
		`import "y" as z | .`,
		`def outer: [now][]; outer`,
		`(now // 0)`,
		`(0 ?// now)`,
		`(@json | now)`,
		`def f: $ENV; f`,
	}
	if len(progs) < 30 {
		t.Fatalf("want at least 30 adversarial programs, got %d", len(progs))
	}
	for _, src := range progs {
		t.Run(src, func(t *testing.T) {
			_, err := CompileMapping(src, nil)
			if err == nil {
				t.Fatal("want compile error")
			}
		})
	}
}

func TestNestedDefNowNeverEvalsWallClock(t *testing.T) {
	src := `def outer: def inner: now; inner; outer`
	m, err := CompileMapping(src, nil)
	if err == nil {
		t.Fatal("want compile error")
	}
	_ = m
}
