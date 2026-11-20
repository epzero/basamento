package local

import (
	"context"
	"fmt"
	goruntime "runtime"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/runtime"
)

// These tests target the window where fireDue completes a timer batch one
// future at a time under r.mu while Select used to scan without that lock.
// Detection is probabilistic: a reader never scheduled inside the window
// cannot observe the bug; the completer yields 100× Gosched to widen it.

func TestSelectAtCallNeverSeesHalfAppliedBatch(t *testing.T) {
	rt, err := New(Options{Clock: coretest.NewFakeClock(time.Unix(1_700_000_000, 0)), IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	for trial := 0; trial < 200; trial++ {
		r := newRun(rt, "acme", fmt.Sprintf("r%d", trial), runtime.FlowRef{Name: "x"}, nil, context.Background())
		first, second := newFuture(r), newFuture(r)
		dc := &durableContext{run: r}
		atSelect := make(chan struct{})
		got := make(chan int, 1)
		r.mu.Lock()
		seq := r.seqs.Add(1)
		second.completeWithSeq(seq, nil, nil, false)
		go func() {
			close(atSelect)
			index, selErr := dc.Select(first, second)
			if selErr != nil {
				got <- -1
				return
			}
			got <- index
		}()
		<-atSelect
		for k := 0; k < 100; k++ {
			goruntime.Gosched()
		}
		first.completeWithSeq(seq, nil, nil, false)
		r.mu.Unlock()
		r.notify()
		index := recvWithin(t, got, "Select result")
		if index != 0 {
			t.Fatalf("trial %d: Select index %d, want 0", trial, index)
		}
	}
}

func TestSelectRescanNeverSeesHalfAppliedBatch(t *testing.T) {
	rt, err := New(Options{Clock: coretest.NewFakeClock(time.Unix(1_700_000_000, 0)), IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	for trial := 0; trial < 200; trial++ {
		r := newRun(rt, "acme", fmt.Sprintf("r%d", trial), runtime.FlowRef{Name: "x"}, nil, context.Background())
		first, second := newFuture(r), newFuture(r)
		dc := &durableContext{run: r}
		atSelect := make(chan struct{})
		completer := make(chan struct{})
		var hookOnce sync.Once
		got := make(chan int, 1)
		hook := &hookFuture{hook: func() {
			hookOnce.Do(func() { close(completer) })
		}}
		go func() {
			close(atSelect)
			index, selErr := dc.Select(first, second, hook)
			if selErr != nil {
				got <- -1
				return
			}
			got <- index
		}()
		<-atSelect
		<-completer
		r.mu.Lock()
		seq := r.seqs.Add(1)
		// Wake the blocked Select so the re-scan can run while the batch is half applied.
		second.completeWithSeq(seq, nil, nil, true)
		for k := 0; k < 500; k++ {
			goruntime.Gosched()
		}
		first.completeWithSeq(seq, nil, nil, false)
		r.mu.Unlock()
		r.notify()
		index := recvWithin(t, got, "Select result")
		if index != 0 {
			t.Fatalf("trial %d: Select index %d, want 0", trial, index)
		}
	}
}
