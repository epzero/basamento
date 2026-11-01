package core_test

import (
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

func TestULIDsConcurrentUnique(t *testing.T) {
	g := core.NewULIDs(coretest.NewFakeClock(time.Unix(0, 0)), nil)
	const workers, each = 8, 500
	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				id, err := g.New("run")
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				if seen[id] {
					t.Errorf("duplicate %s", id)
				}
				seen[id] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != workers*each {
		t.Fatalf("got %d ids", len(seen))
	}
}

func TestErrorCodes(t *testing.T) {
	err := core.NewError(core.CodeBudgetExceeded, "over", nil)
	if core.CodeOf(err) != core.CodeBudgetExceeded || core.IsRetryable(err) {
		t.Fatalf("unexpected classification of %v", err)
	}
	if core.CodeOf(nil) != core.CodeInternal {
		t.Fatal("unknown errors map to internal")
	}
}
