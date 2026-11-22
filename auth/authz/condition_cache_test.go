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
