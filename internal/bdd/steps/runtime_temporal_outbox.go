package steps

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

type memOutbox struct {
	mu           sync.Mutex
	seen         map[uint64]runtime.ProgressEvent
	nextSeq      uint64
	activityDone *atomic.Uint64
}

func newMemOutbox() *memOutbox {
	return &memOutbox{seen: map[uint64]runtime.ProgressEvent{}}
}

func memOutboxRecord(ctx context.Context, run runtime.RunRef, seq uint64, ev runtime.ProgressEvent) error {
	ctxTenant, ok := core.TenantFrom(ctx)
	if !ok || ctxTenant == "" {
		return core.NewError(core.CodeInvalidArgument, "temporal: emit context missing tenant", nil)
	}
	if run.TenantID == "" {
		return core.NewError(core.CodeInvalidArgument, "temporal: emit run ref missing tenant", nil)
	}
	if run.TenantID != ctxTenant {
		return core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("temporal: emit tenant mismatch context=%q run_ref=%q", ctxTenant, run.TenantID), nil)
	}
	return nil
}

func (o *memOutbox) Append(ctx context.Context, run runtime.RunRef, seq uint64, ev runtime.ProgressEvent) error {
	if err := memOutboxRecord(ctx, run, seq, ev); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if seq > o.nextSeq+1 {
		return fmt.Errorf("temporal bdd outbox: seq %d leaves a gap after %d", seq, o.nextSeq)
	}
	if _, exists := o.seen[seq]; exists {
		return fmt.Errorf("temporal bdd outbox: duplicate append for seq %d", seq)
	}
	if seq == o.nextSeq+1 {
		o.nextSeq = seq
	}
	o.seen[seq] = runtime.ProgressEvent{Type: ev.Type, Data: append(ev.Data[:0:0], ev.Data...)}
	if o.activityDone != nil {
		o.activityDone.Add(1)
	}
	return nil
}

func (o *memOutbox) eventTypesDense() ([]string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.nextSeq == 0 {
		return nil, nil
	}
	out := make([]string, 0, o.nextSeq)
	for i := uint64(1); i <= o.nextSeq; i++ {
		ev, ok := o.seen[i]
		if !ok {
			return nil, fmt.Errorf("temporal bdd outbox: missing seq %d", i)
		}
		out = append(out, ev.Type)
	}
	return out, nil
}

func (w *temporalWorld) bddOutbox() *memOutbox {
	if w.outbox == nil {
		w.outbox = newMemOutbox()
		w.outbox.activityDone = &w.execActivityDone
	}
	return w.outbox
}
