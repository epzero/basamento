package temporal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

type testOutbox struct {
	mu         sync.Mutex
	seen       map[uint64]outboxAppendRecord
	calls      []outboxAppendRecord
	wantTenant core.TenantID
}

type outboxAppendRecord struct {
	seq    uint64
	typ    string
	runID  string
	tenant core.TenantID
	data   json.RawMessage
}

func newTestOutbox() *testOutbox {
	return &testOutbox{seen: map[uint64]outboxAppendRecord{}, wantTenant: "acme"}
}

// checkRun asserts the run ref has the test tenant and a bare run id, so a
// forged or unbound run id is caught instead of silently stored. The exact run
// id is not compared: tests start runs with different ids, and a duplicate seq
// is checked against the stored call's run id.
func (o *testOutbox) checkRun(run runtime.RunRef) error {
	if o.wantTenant != "" && run.TenantID != o.wantTenant {
		return core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("temporal: run tenant %q is not the test tenant %q", run.TenantID, o.wantTenant), nil)
	}
	if run.RunID == "" || strings.ContainsRune(run.RunID, '/') {
		return core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("temporal: run id %q is not a bare run id", run.RunID), nil)
	}
	return nil
}

func outboxRecord(ctx context.Context, run runtime.RunRef, seq uint64, ev runtime.ProgressEvent) (outboxAppendRecord, error) {
	ctxTenant, ok := core.TenantFrom(ctx)
	if !ok || ctxTenant == "" {
		return outboxAppendRecord{}, core.NewError(core.CodeInvalidArgument, "temporal: emit context missing tenant", nil)
	}
	if run.TenantID == "" {
		return outboxAppendRecord{}, core.NewError(core.CodeInvalidArgument, "temporal: emit run ref missing tenant", nil)
	}
	if run.TenantID != ctxTenant {
		return outboxAppendRecord{}, core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("temporal: emit tenant mismatch context=%q run_ref=%q", ctxTenant, run.TenantID), nil)
	}
	data := append(json.RawMessage(nil), ev.Data...)
	return outboxAppendRecord{
		seq: seq, typ: ev.Type, runID: run.RunID, tenant: run.TenantID, data: data,
	}, nil
}

func (o *testOutbox) recordCall(call outboxAppendRecord) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, call)
}

func (o *testOutbox) commitSeen(call outboxAppendRecord, ev runtime.ProgressEvent) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if prev, ok := o.seen[call.seq]; ok {
		// Idempotent per seq, but a seq reused for a different event (or by a
		// different run) is a bug, not a retry.
		if prev.typ != call.typ || prev.runID != call.runID || !bytes.Equal(prev.data, call.data) {
			return core.NewError(core.CodeConflict,
				fmt.Sprintf("temporal: seq %d reused for a different event in run %q", call.seq, call.runID), nil)
		}
		return nil
	}
	o.seen[call.seq] = outboxAppendRecord{seq: call.seq, typ: ev.Type, runID: call.runID, tenant: call.tenant, data: call.data}
	return nil
}

// storeCall records the attempt and commits to seen (successful Append only).
func (o *testOutbox) storeCall(call outboxAppendRecord, ev runtime.ProgressEvent) error {
	o.recordCall(call)
	return o.commitSeen(call, ev)
}

func (o *testOutbox) appendStrict(ctx context.Context, run runtime.RunRef, seq uint64, ev runtime.ProgressEvent) error {
	if err := o.checkRun(run); err != nil {
		return err
	}
	call, err := outboxRecord(ctx, run, seq, ev)
	if err != nil {
		return err
	}
	return o.storeCall(call, ev)
}

func (o *testOutbox) Append(ctx context.Context, run runtime.RunRef, seq uint64, ev runtime.ProgressEvent) error {
	return o.appendStrict(ctx, run, seq, ev)
}

func (o *testOutbox) types() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var max uint64
	for k := range o.seen {
		if k > max {
			max = k
		}
	}
	out := make([]string, 0, max)
	for i := uint64(1); i <= max; i++ {
		if rec, ok := o.seen[i]; ok {
			out = append(out, rec.typ)
		}
	}
	return out
}

func (o *testOutbox) callsCopy() []outboxAppendRecord {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]outboxAppendRecord, len(o.calls))
	copy(out, o.calls)
	return out
}

type recordingOutbox struct {
	*testOutbox
	failFirst int
	failErr   *core.Error
	failCount int
}

func newRecordingOutbox() *recordingOutbox {
	return &recordingOutbox{testOutbox: newTestOutbox()}
}

func (o *recordingOutbox) Append(ctx context.Context, run runtime.RunRef, seq uint64, ev runtime.ProgressEvent) error {
	if err := o.checkRun(run); err != nil {
		return err
	}
	call, err := outboxRecord(ctx, run, seq, ev)
	if err != nil {
		return err
	}
	o.recordCall(call)
	if o.failCount < o.failFirst && o.failErr != nil {
		o.failCount++
		return o.failErr
	}
	return o.commitSeen(call, ev)
}

func testOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		Payloads: testSecretsCodec(t),
		Blobs:    testBlobStore(t),
		Outbox:   newTestOutbox(),
	}
}

func TestTestOutboxRejectsSeqReusedForDifferentEvent(t *testing.T) {
	o := newTestOutbox()
	ctx := core.WithTenant(context.Background(), "acme")
	run := runtime.RunRef{RunID: "r1", TenantID: "acme"}
	if err := o.Append(ctx, run, 1, runtime.ProgressEvent{Type: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := o.Append(ctx, run, 1, runtime.ProgressEvent{Type: "a"}); err != nil {
		t.Fatalf("same event retry: %v", err)
	}
	if err := o.Append(ctx, run, 1, runtime.ProgressEvent{Type: "b"}); core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("different event error %v, want conflict", err)
	}
	other := runtime.RunRef{RunID: "r2", TenantID: "acme"}
	if err := o.Append(ctx, other, 1, runtime.ProgressEvent{Type: "a"}); core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("different run error %v, want conflict", err)
	}
}
