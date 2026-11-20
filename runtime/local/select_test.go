package local

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	goruntime "runtime"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/runtime"
)

func TestSelectReturnsLowestReadyIndex(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	started := make(chan struct{})
	type outcome struct {
		index int
		err   error
	}
	got := make(chan outcome, 1)
	f.flow("select", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		first := dc.Sleep(2 * time.Hour)
		second := dc.Sleep(time.Hour)
		close(started)
		index, err := dc.Select(first, second)
		got <- outcome{index: index, err: err}
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "select", `{}`)
	waitOn(t, "the timers to register", started)
	f.clock.Advance(time.Hour)
	result := recvWithin(t, got, "Select result")
	if result.err != nil || result.index != 1 {
		t.Fatalf("Select = (%d, %v), want (1, nil)", result.index, result.err)
	}
	f.waitState(t, "acme", "r1", stateCompleted)
}

func TestSelectIsDeterministicOverReadySubsets(t *testing.T) {
	rt, err := New(Options{Clock: coretest.NewFakeClock(time.Unix(1_700_000_000, 0)), IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(7, 11))
	for trial := 0; trial < 200; trial++ {
		r := newRun(rt, "acme", fmt.Sprintf("r%d", trial), runtime.FlowRef{Name: "x"}, nil, context.Background())
		fs := make([]runtime.Future, 8)
		ready := map[int]bool{}
		for i := range fs {
			fs[i] = newFuture(r)
			if rng.IntN(2) == 0 {
				ready[i] = true
				fs[i].(*future).complete(nil, nil)
			}
		}
		if len(ready) == 0 {
			ready[0] = true
			fs[0].(*future).complete(nil, nil)
		}
		index, err := (&durableContext{run: r}).Select(fs...)
		if err != nil {
			t.Fatalf("trial %d: Select: %v", trial, err)
		}
		if !ready[index] {
			t.Fatalf("trial %d: Select returned unready index %d (ready %v)", trial, index, ready)
		}
		for i := range fs {
			if ready[i] && i < index {
				t.Fatalf("trial %d: Select returned %d but %d is ready", trial, index, i)
			}
		}
	}
}

func TestSelectPrefersTheFutureThatCompletedFirst(t *testing.T) {
	rt, err := New(Options{Clock: coretest.NewFakeClock(time.Unix(1_700_000_000, 0)), IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	r := newRun(rt, "acme", "r1", runtime.FlowRef{Name: "x"}, nil, context.Background())
	first, second := newFuture(r), newFuture(r)

	second.complete(nil, nil)
	first.complete(nil, nil)

	if index := r.firstCompleted([]runtime.Future{first, second}); index != 1 {
		t.Fatalf("firstCompleted = %d, want 1 (earliest seq)", index)
	}

	third, fourth := newFuture(r), newFuture(r)
	seq := r.seqs.Add(1)
	third.completeWithSeq(seq, nil, nil, true)
	fourth.completeWithSeq(seq, nil, nil, true)
	if index := r.firstCompleted([]runtime.Future{third, fourth}); index != 0 {
		t.Fatalf("equal seq firstCompleted = %d, want 0", index)
	}
}

// hookFuture runs hook on its first Ready() call and always reports not ready.
type hookFuture struct {
	once sync.Once
	hook func()
}

func (h *hookFuture) Get(any) error { return fmt.Errorf("hookFuture: unexpected Get") }
func (h *hookFuture) Ready() bool {
	h.once.Do(h.hook)
	return false
}

func TestSelectNoLostWakeCompletingAfterReadyScan(t *testing.T) {
	rt, err := New(Options{Clock: coretest.NewFakeClock(time.Unix(1_700_000_000, 0)), IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	r := newRun(rt, "acme", "r1", runtime.FlowRef{Name: "x"}, nil, context.Background())
	t.Cleanup(r.cancel)
	dc := &durableContext{run: r}
	f0 := newFuture(r)
	seq := r.seqs.Add(1)
	hook := &hookFuture{hook: func() {
		f0.completeWithSeq(seq, nil, nil, true)
	}}
	got := make(chan int, 1)
	go func() {
		index, selErr := dc.Select(f0, hook)
		if selErr != nil {
			t.Errorf("Select: %v", selErr)
			got <- -1
			return
		}
		got <- index
	}()
	if index := recvWithin(t, got, "Select result"); index != 0 {
		t.Fatalf("Select index %d, want 0", index)
	}
}

func TestSelectNoLostWakeFireDueBatchWindow(t *testing.T) {
	rt, err := New(Options{Clock: coretest.NewFakeClock(time.Unix(1_700_000_000, 0)), IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	r := newRun(rt, "acme", "r1", runtime.FlowRef{Name: "x"}, nil, context.Background())
	t.Cleanup(r.cancel)
	dc := &durableContext{run: r}
	f0 := newFuture(r)
	batchSeq := r.seqs.Add(1)
	hook := &hookFuture{hook: func() {
		f0.completeWithSeq(batchSeq, nil, nil, true)
	}}
	got := make(chan int, 1)
	go func() {
		index, selErr := dc.Select(f0, hook)
		if selErr != nil {
			t.Errorf("Select: %v", selErr)
			got <- -1
			return
		}
		got <- index
	}()
	if index := recvWithin(t, got, "Select result"); index != 0 {
		t.Fatalf("Select index %d, want 0", index)
	}
}

func TestFireDueBatchSameSeq(t *testing.T) {
	rt, err := New(Options{Clock: coretest.NewFakeClock(time.Unix(1_700_000_000, 0)), IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	r := newRun(rt, "acme", "r1", runtime.FlowRef{Name: "x"}, nil, context.Background())
	first, second := newFuture(r), newFuture(r)
	deadline := rt.clock.Now()
	timers := []*runTimer{
		{deadline: deadline, future: first},
		{deadline: deadline, future: second},
	}
	r.mu.Lock()
	r.timers = timers
	r.mu.Unlock()
	r.fireDue(timers[1])

	s0, ok0 := first.completedSeq()
	s1, ok1 := second.completedSeq()
	if !ok0 || !ok1 {
		t.Fatal("futures not complete")
	}
	if s0 != s1 {
		t.Fatalf("seq %d and %d, want the same batch seq", s0, s1)
	}
	index, err := (&durableContext{run: r}).Select(first, second)
	if err != nil {
		t.Fatal(err)
	}
	if index != 0 {
		t.Fatalf("Select index %d, want 0", index)
	}
}

func testSelectSameAdvanceOrder(t *testing.T, longFirst bool) {
	t.Helper()
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	got := make(chan int, 1)
	started := make(chan struct{})
	f.flow("select", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var first, second runtime.Future
		if longFirst {
			first = dc.Sleep(2 * time.Hour)
			second = dc.Sleep(time.Hour)
		} else {
			second = dc.Sleep(2 * time.Hour)
			first = dc.Sleep(time.Hour)
		}
		close(started)
		index, err := dc.Select(first, second)
		if err != nil {
			return nil, err
		}
		got <- index
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "select", `{}`)
	waitOn(t, "timers", started)
	f.clock.Advance(2 * time.Hour)
	index := recvWithin(t, got, "Select result")
	if index != 0 {
		t.Fatalf("Select index %d, want 0 (lowest when both timers fire together)", index)
	}
}

func TestSelectSameAdvanceReturnsLowestIndex(t *testing.T) {
	t.Run("long_first", func(t *testing.T) { testSelectSameAdvanceOrder(t, true) })
	t.Run("long_second", func(t *testing.T) { testSelectSameAdvanceOrder(t, false) })
}

func TestSelectSameAdvanceLowestIndexUnderSchedulerNoise(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for trial := 0; trial < 200; trial++ {
		f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
		got := make(chan int, 1)
		started := make(chan struct{})
		f.flow("select", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
			first := dc.Sleep(2 * time.Hour)
			second := dc.Sleep(time.Hour)
			close(started)
			index, err := dc.Select(first, second)
			if err != nil {
				return nil, err
			}
			got <- index
			return json.RawMessage(`{}`), nil
		})
		f.start(t, "acme", "r1", "select", `{}`)
		waitOn(t, "timers", started)
		f.clock.Advance(2 * time.Hour)
		for i := 0; i < rng.IntN(20)+1; i++ {
			goruntime.Gosched()
		}
		index := recvWithin(t, got, "Select result")
		f.close(t)
		if index != 0 {
			t.Fatalf("trial %d: Select index %d, want 0", trial, index)
		}
	}
}

func TestSelectReadyAtCallBeatsLaterReady(t *testing.T) {
	rt, err := New(Options{Clock: coretest.NewFakeClock(time.Unix(1_700_000_000, 0)), IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	r := newRun(rt, "acme", "r1", runtime.FlowRef{Name: "x"}, nil, context.Background())
	late, early := newFuture(r), newFuture(r)
	early.complete(nil, nil)
	index, err := (&durableContext{run: r}).Select(late, early)
	if err != nil {
		t.Fatal(err)
	}
	if index != 1 {
		t.Fatalf("Select index %d, want 1 (higher index ready at call)", index)
	}
}

func TestSelectReadyAtCallUsesLowestIndex(t *testing.T) {
	rt, err := New(Options{Clock: coretest.NewFakeClock(time.Unix(1_700_000_000, 0)), IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	r := newRun(rt, "acme", "r1", runtime.FlowRef{Name: "x"}, nil, context.Background())
	first, second := newFuture(r), newFuture(r)
	first.complete(nil, nil)
	second.complete(nil, nil)
	index, err := (&durableContext{run: r}).Select(first, second)
	if err != nil {
		t.Fatal(err)
	}
	if index != 0 {
		t.Fatalf("Select index %d, want 0", index)
	}
}
