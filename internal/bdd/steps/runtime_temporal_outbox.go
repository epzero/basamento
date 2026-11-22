package steps

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

type memOutbox struct {
	mu           sync.Mutex
	seen         map[runtime.RunRef]map[uint64]runtime.ProgressEvent
	activityDone *atomic.Uint64
	wantTenant   core.TenantID
	wantRun      runtime.RunRef
}

func newMemOutbox() *memOutbox {
	return &memOutbox{
		seen: map[runtime.RunRef]map[uint64]runtime.ProgressEvent{},
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

// checkRun asserts the run ref has the scenario's tenant and a bare run id, so
// a forged or unbound run id is caught instead of silently stored. The exact
// run id is not compared: the scenario checks the stored events, and a fresh
// run id may legitimately differ from wantRun.
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
	if prev, exists := seen[seq]; exists {
		// The outbox contract makes Append idempotent per seq: an activity retry
		// or workflow replay must not store a second copy. The same seq carrying
		// a different event is a bug (a reused seq), not a retry.
		if prev.Type != ev.Type || !bytes.Equal(prev.Data, ev.Data) {
			return core.NewError(core.CodeConflict,
				fmt.Sprintf("temporal: seq %d reused for a different event in run %q", seq, run.RunID), nil)
		}
		return nil
	}
	seen[seq] = runtime.ProgressEvent{Type: ev.Type, Data: append(ev.Data[:0:0], ev.Data...)}
	if o.activityDone != nil {
		o.activityDone.Add(1)
	}
	return nil
}

// eventsInSeqOrder returns the run's stored events ordered by seq, with no
// requirement that the seqs are contiguous: the contract is idempotency by
// seq, not dense numbering.
func (o *memOutbox) eventsInSeqOrder() []runtime.ProgressEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	seen := o.seen[o.wantRun]
	seqs := make([]uint64, 0, len(seen))
	for seq := range seen {
		seqs = append(seqs, seq)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	out := make([]runtime.ProgressEvent, 0, len(seqs))
	for _, seq := range seqs {
		out = append(out, seen[seq])
	}
	return out
}

func (w *temporalWorld) bddOutbox() *memOutbox {
	if w.outbox == nil {
		w.outbox = newMemOutbox()
		w.outbox.activityDone = &w.execActivityDone
	}
	return w.outbox
}
