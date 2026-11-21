package steps

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

type memOutbox struct {
	mu           sync.Mutex
	seen         map[runtime.RunRef]map[uint64]runtime.ProgressEvent
	nextSeq      map[runtime.RunRef]uint64
	activityDone *atomic.Uint64
	wantTenant   core.TenantID
	wantRun      runtime.RunRef
}

func newMemOutbox() *memOutbox {
	return &memOutbox{
		seen:    map[runtime.RunRef]map[uint64]runtime.ProgressEvent{},
		nextSeq: map[runtime.RunRef]uint64{},
	}
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

// checkRun asserts the sink is handed the run the scenario started, so a
// forged or unbound run id is caught instead of silently stored.
func (o *memOutbox) checkRun(run runtime.RunRef) error {
	if o.wantTenant != "" && run.TenantID != o.wantTenant {
		return core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("temporal: run tenant %q is not the scenario tenant %q", run.TenantID, o.wantTenant), nil)
	}
	if run.RunID == "" || strings.ContainsRune(run.RunID, '/') {
		return core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("temporal: run id %q is not a bare run id", run.RunID), nil)
	}
	return nil
}

func (o *memOutbox) Append(ctx context.Context, run runtime.RunRef, seq uint64, ev runtime.ProgressEvent) error {
	if err := o.checkRun(run); err != nil {
		return err
	}
	if err := memOutboxRecord(ctx, run, seq, ev); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	seen := o.seen[run]
	if seen == nil {
		seen = map[uint64]runtime.ProgressEvent{}
		o.seen[run] = seen
	}
	next := o.nextSeq[run]
	if seq > next+1 {
		return fmt.Errorf("temporal bdd outbox: seq %d leaves a gap after %d for run %q", seq, next, run.RunID)
	}
	if _, exists := seen[seq]; exists {
		// The outbox contract makes Append idempotent per seq: an activity retry
		// or workflow replay must not store a second copy.
		return nil
	}
	if seq == next+1 {
		o.nextSeq[run] = seq
	}
	seen[seq] = runtime.ProgressEvent{Type: ev.Type, Data: append(ev.Data[:0:0], ev.Data...)}
	if o.activityDone != nil {
		o.activityDone.Add(1)
	}
	return nil
}

func (o *memOutbox) eventTypesDense() ([]string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	next := o.nextSeq[o.wantRun]
	if next == 0 {
		return nil, nil
	}
	seen := o.seen[o.wantRun]
	out := make([]string, 0, next)
	for i := uint64(1); i <= next; i++ {
		ev, ok := seen[i]
		if !ok {
			return nil, fmt.Errorf("temporal bdd outbox: missing seq %d for run %q", i, o.wantRun.RunID)
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
