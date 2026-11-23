package authz

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/epzero/basamento/expr"
)

func TestConditionCacheBound(t *testing.T) {
	c := newConditionCache(2)
	comp, err := newConditionCompiler(defaultCostLimit)
	if err != nil {
		t.Fatal(err)
	}
	compile := comp.compileNew
	for _, src := range []string{`true`, `false`, `1 == 1`} {
		if _, err := c.get(src, compile); err != nil {
			t.Fatal(err)
		}
	}
	if c.len() != 2 {
		t.Fatalf("cache len %d want 2", c.len())
	}
}

func TestConditionCacheConcurrent(t *testing.T) {
	c := newConditionCache(32)
	comp, err := newConditionCompiler(defaultCostLimit)
	if err != nil {
		t.Fatal(err)
	}
	compile := comp.compileNew
	const workers = 16
	type entry struct {
		p   *expr.Program
		err error
	}
	got := make([]entry, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			src := `resource.id == "r"`
			if n%2 == 0 {
				src = `resource.id == "x"`
			}
			p, err := c.get(src, compile)
			got[n] = entry{p: p, err: err}
		}(i)
	}
	wg.Wait()
	for i, e := range got {
		if e.err != nil || e.p == nil {
			t.Fatalf("worker %d: p=%v err=%v", i, e.p, e.err)
		}
	}
	if c.len() != 2 {
		t.Fatalf("cache len %d want 2", c.len())
	}
	pR, err := c.get(`resource.id == "r"`, compile)
	if err != nil || pR == nil {
		t.Fatalf("r: %v", err)
	}
	pX, err := c.get(`resource.id == "x"`, compile)
	if err != nil || pX == nil {
		t.Fatalf("x: %v", err)
	}
	for i, e := range got {
		want := pR
		if i%2 == 0 {
			want = pX
		}
		if e.p != want {
			t.Fatalf("worker %d program pointer", i)
		}
	}
}

func TestConditionCacheCompilesOutsideLock(t *testing.T) {
	c := newConditionCache(4)
	base, err := newConditionCompiler(defaultCostLimit)
	if err != nil {
		t.Fatal(err)
	}
	compile := func(text string) (*expr.Program, error) {
		if !c.mu.TryLock() {
			t.Fatal("compile called while cache lock held")
		}
		c.mu.Unlock()
		return base.compileNew(text)
	}
	if _, err := c.get(`true`, compile); err != nil {
		t.Fatal(err)
	}
}

func TestConditionCacheConcurrentSameText(t *testing.T) {
	c := newConditionCache(8)
	base, err := newConditionCompiler(defaultCostLimit)
	if err != nil {
		t.Fatal(err)
	}
	var entered int32
	release := make(chan struct{})
	compile := func(text string) (*expr.Program, error) {
		if atomic.AddInt32(&entered, 1) == 2 {
			close(release)
		}
		<-release
		return base.compileNew(text)
	}
	const workers = 2
	progs := make([]*expr.Program, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	text := `resource.id == "shared"`
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			progs[n], errs[n] = c.get(text, compile)
		}(i)
	}
	wg.Wait()
	for i := 0; i < workers; i++ {
		if errs[i] != nil || progs[i] == nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
	}
	if progs[0] != progs[1] {
		t.Fatal("concurrent miss stored different programs")
	}
	if c.len() != 1 {
		t.Fatalf("cache len %d want 1", c.len())
	}
}

func TestConditionCacheCompileErrorShared(t *testing.T) {
	c := newConditionCache(4)
	fail := errors.New("compile failed")
	compile := func(string) (*expr.Program, error) {
		return nil, fail
	}
	bad := `not valid cel`
	_, err1 := c.get(bad, compile)
	_, err2 := c.get(bad, compile)
	if !errors.Is(err1, fail) || !errors.Is(err2, fail) {
		t.Fatalf("errors %v %v", err1, err2)
	}
	if c.len() != 1 {
		t.Fatalf("cache len %d want 1", c.len())
	}
}

func assertCacheMapsAligned(t *testing.T, c *conditionCache) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if got := len(c.m) + len(c.err); got != len(c.order) {
		t.Fatalf("map slots %d order %d", got, len(c.order))
	}
	for _, k := range c.order {
		_, inM := c.m[k]
		_, inE := c.err[k]
		if inM == inE {
			t.Fatalf("key %q in both or neither maps", k)
		}
	}
}

func TestConditionCacheErrorsAreBounded(t *testing.T) {
	c := newConditionCache(2)
	known := map[string]error{
		"bad-1": errors.New("fail-1"),
		"bad-2": errors.New("fail-2"),
		"bad-3": errors.New("fail-3"),
	}
	counts := map[string]int{}
	compile := func(text string) (*expr.Program, error) {
		counts[text]++
		err, ok := known[text]
		if !ok {
			return nil, errors.New("unknown text")
		}
		return nil, err
	}
	for _, text := range []string{"bad-1", "bad-2", "bad-3"} {
		if _, err := c.get(text, compile); !errors.Is(err, known[text]) {
			t.Fatalf("%s: %v", text, err)
		}
	}
	if c.len() != 2 {
		t.Fatalf("len %d want 2", c.len())
	}
	assertCacheMapsAligned(t, c)
	for _, text := range []string{"bad-2", "bad-3"} {
		prev := counts[text]
		if _, err := c.get(text, compile); !errors.Is(err, known[text]) {
			t.Fatalf("%s: %v", text, err)
		}
		if counts[text] != prev {
			t.Fatalf("%s compile calls %d want %d", text, counts[text], prev)
		}
	}
	before := counts["bad-1"]
	if _, err := c.get("bad-1", compile); !errors.Is(err, known["bad-1"]) {
		t.Fatalf("bad-1 again: %v", err)
	}
	if counts["bad-1"] != before+1 {
		t.Fatalf("bad-1 compile calls %d want %d", counts["bad-1"], before+1)
	}
}

func TestConditionCacheHitRefreshesRecency(t *testing.T) {
	base, err := newConditionCompiler(defaultCostLimit)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("success", func(t *testing.T) {
		c := newConditionCache(2)
		counts := map[string]int{}
		compile := func(text string) (*expr.Program, error) {
			counts[text]++
			return base.compileNew(text)
		}
		srcA, srcB, srcC := `true`, `false`, `1 == 1`
		if _, err := c.get(srcA, compile); err != nil {
			t.Fatal(err)
		}
		if _, err := c.get(srcB, compile); err != nil {
			t.Fatal(err)
		}
		if _, err := c.get(srcA, compile); err != nil {
			t.Fatal(err)
		}
		if counts[srcA] != 1 || counts[srcB] != 1 {
			t.Fatalf("counts after refresh %+v", counts)
		}
		if _, err := c.get(srcC, compile); err != nil {
			t.Fatal(err)
		}
		if counts[srcB] != 1 {
			t.Fatalf("srcB compile calls %d want 1", counts[srcB])
		}
		if _, err := c.get(srcA, compile); err != nil {
			t.Fatal(err)
		}
		if counts[srcA] != 1 {
			t.Fatalf("srcA compile calls %d want 1", counts[srcA])
		}
		prevB := counts[srcB]
		if _, err := c.get(srcB, compile); err != nil {
			t.Fatal(err)
		}
		if counts[srcB] != prevB+1 {
			t.Fatalf("srcB compile calls %d want %d", counts[srcB], prevB+1)
		}
	})
	t.Run("error", func(t *testing.T) {
		c := newConditionCache(2)
		errs := map[string]error{
			"e-a": errors.New("err-a"),
			"e-b": errors.New("err-b"),
			"e-c": errors.New("err-c"),
		}
		counts := map[string]int{}
		compile := func(text string) (*expr.Program, error) {
			counts[text]++
			e, ok := errs[text]
			if !ok {
				return nil, errors.New("missing")
			}
			return nil, e
		}
		for _, k := range []string{"e-a", "e-b"} {
			if _, err := c.get(k, compile); !errors.Is(err, errs[k]) {
				t.Fatal(err)
			}
		}
		if _, err := c.get("e-a", compile); !errors.Is(err, errs["e-a"]) {
			t.Fatal(err)
		}
		if counts["e-a"] != 1 || counts["e-b"] != 1 {
			t.Fatalf("counts %+v", counts)
		}
		if _, err := c.get("e-c", compile); !errors.Is(err, errs["e-c"]) {
			t.Fatal(err)
		}
		if counts["e-b"] != 1 {
			t.Fatalf("e-b calls %d", counts["e-b"])
		}
		if _, err := c.get("e-a", compile); !errors.Is(err, errs["e-a"]) {
			t.Fatal(err)
		}
		if counts["e-a"] != 1 {
			t.Fatalf("e-a calls %d", counts["e-a"])
		}
		prev := counts["e-b"]
		if _, err := c.get("e-b", compile); !errors.Is(err, errs["e-b"]) {
			t.Fatal(err)
		}
		if counts["e-b"] != prev+1 {
			t.Fatalf("e-b calls %d want %d", counts["e-b"], prev+1)
		}
	})
}

func TestConditionCacheSecondChanceReentrant(t *testing.T) {
	base, err := newConditionCompiler(defaultCostLimit)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("success", func(t *testing.T) {
		c := newConditionCache(4)
		text := `resource.id == "reentrant"`
		var innerP *expr.Program
		outerCalls := 0
		outer := func(text string) (*expr.Program, error) {
			outerCalls++
			inner := func(string) (*expr.Program, error) {
				return base.compileNew(`true`)
			}
			p, err := c.get(text, inner)
			if err != nil {
				return nil, err
			}
			innerP = p
			return nil, errors.New("outer compile unused")
		}
		got, err := c.get(text, outer)
		if err != nil || got == nil {
			t.Fatalf("got %v err %v", got, err)
		}
		if got != innerP {
			t.Fatal("outer returned different program than inner stored")
		}
		if outerCalls != 1 {
			t.Fatalf("outer calls %d", outerCalls)
		}
		if c.len() != 1 {
			t.Fatalf("len %d", c.len())
		}
	})
	t.Run("error", func(t *testing.T) {
		c := newConditionCache(4)
		text := `bad-reentrant`
		innerErr := errors.New("inner fail")
		outer := func(text string) (*expr.Program, error) {
			inner := func(string) (*expr.Program, error) {
				return nil, innerErr
			}
			_, err := c.get(text, inner)
			return nil, err
		}
		_, err := c.get(text, outer)
		if !errors.Is(err, innerErr) {
			t.Fatalf("err %v", err)
		}
		if c.len() != 1 {
			t.Fatalf("len %d", c.len())
		}
		c.mu.Lock()
		if _, ok := c.err[text]; !ok {
			t.Fatal("error not cached")
		}
		c.mu.Unlock()
	})
	t.Run("second_chance_touch_keeps_recency", func(t *testing.T) {
		c := newConditionCache(2)
		src := map[string]string{
			"key-o": `false`,
			"key-t": `true`,
			"key-e": `1 == 1`,
		}
		compileKey := func(text string) (*expr.Program, error) {
			return base.compileNew(src[text])
		}
		const prime, target, extra = "key-o", "key-t", "key-e"
		if _, err := c.get(prime, compileKey); err != nil {
			t.Fatal(err)
		}
		outer := func(text string) (*expr.Program, error) {
			inner := func(string) (*expr.Program, error) {
				return base.compileNew(src[text])
			}
			if _, err := c.get(text, inner); err != nil {
				return nil, err
			}
			if _, err := c.get(prime, compileKey); err != nil {
				return nil, err
			}
			return nil, errors.New("outer unused")
		}
		if _, err := c.get(target, outer); err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{}
		counting := func(text string) (*expr.Program, error) {
			counts[text]++
			return base.compileNew(src[text])
		}
		if _, err := c.get(extra, counting); err != nil {
			t.Fatal(err)
		}
		if _, err := c.get(target, counting); err != nil {
			t.Fatal(err)
		}
		if counts[target] != 0 {
			t.Fatalf("target compile calls %d", counts[target])
		}
		prev := counts[prime]
		if _, err := c.get(prime, counting); err != nil {
			t.Fatal(err)
		}
		if counts[prime] != prev+1 {
			t.Fatalf("prime compile calls %d want %d", counts[prime], prev+1)
		}
	})
	t.Run("second_chance_error_touch_keeps_recency", func(t *testing.T) {
		c := newConditionCache(2)
		errs := map[string]error{
			"key-o": errors.New("err-o"),
			"key-t": errors.New("err-t"),
			"key-e": errors.New("err-e"),
		}
		fail := func(text string) (*expr.Program, error) {
			return nil, errs[text]
		}
		const prime, target, extra = "key-o", "key-t", "key-e"
		if _, err := c.get(prime, fail); !errors.Is(err, errs[prime]) {
			t.Fatal(err)
		}
		outer := func(text string) (*expr.Program, error) {
			inner := func(string) (*expr.Program, error) {
				return nil, errs[text]
			}
			_, _ = c.get(text, inner)
			if _, err := c.get(prime, fail); err != nil {
				return nil, err
			}
			return nil, errors.New("outer unused")
		}
		if _, err := c.get(target, outer); !errors.Is(err, errs[target]) {
			t.Fatal(err)
		}
		counts := map[string]int{}
		counting := func(text string) (*expr.Program, error) {
			counts[text]++
			return nil, errs[text]
		}
		if _, err := c.get(extra, counting); !errors.Is(err, errs[extra]) {
			t.Fatal(err)
		}
		if _, err := c.get(target, counting); !errors.Is(err, errs[target]) {
			t.Fatal(err)
		}
		if counts[target] != 0 {
			t.Fatalf("target compile calls %d", counts[target])
		}
		prev := counts[prime]
		if _, err := c.get(prime, counting); !errors.Is(err, errs[prime]) {
			t.Fatal(err)
		}
		if counts[prime] != prev+1 {
			t.Fatalf("prime compile calls %d want %d", counts[prime], prev+1)
		}
	})
}
