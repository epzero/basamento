package authz

import (
	"sync"
	"testing"
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
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			src := `resource.id == "r"`
			if n%2 == 0 {
				src = `resource.id == "x"`
			}
			_, _ = c.get(src, comp.compileNew)
		}(i)
	}
	wg.Wait()
}
