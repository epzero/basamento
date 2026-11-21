package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/runtime"
)

type emitFailureRecord struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func assertOutboxAppendsEqual(t *testing.T, got []outboxAppendRecord, want []outboxAppendRecord) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("calls len %d want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].seq != want[i].seq || got[i].typ != want[i].typ {
			t.Fatalf("call[%d] %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestEmitOrderAndSeq(t *testing.T) {
	ob := newRecordingOutbox()
	opts := testOptions(t)
	opts.Outbox = ob
	rt, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		if err := dc.Emit(runtime.ProgressEvent{Type: "a"}); err != nil {
			return nil, err
		}
		if err := dc.Emit(runtime.ProgressEvent{Type: "b"}); err != nil {
			return nil, err
		}
		return nil, nil
	})
	runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "e"}, RunID: "r1"}, defaultChainOpts())
	assertOutboxAppendsEqual(t, ob.callsCopy(), []outboxAppendRecord{{seq: 1, typ: "a"}, {seq: 2, typ: "b"}})
}

func TestEmitRetryableSinkErrorIsRetriedThenStoredOnce(t *testing.T) {
	ob := newRecordingOutbox()
	ob.failFirst = 1
	ob.failErr = &core.Error{Code: core.CodeUnavailable, Message: "down", Retryable: true}
	opts := testOptions(t)
	opts.Outbox = ob
	rt, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		return nil, dc.Emit(runtime.ProgressEvent{Type: "a"})
	})
	runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "e"}, RunID: "r1"}, defaultChainOpts())
	assertOutboxAppendsEqual(t, ob.callsCopy(), []outboxAppendRecord{{seq: 1, typ: "a"}, {seq: 1, typ: "a"}})
	types := ob.types()
	if len(types) != 1 || types[0] != "a" {
		t.Fatalf("types %+v want single stored a", types)
	}
}

func TestEmitNonRetryableSinkErrorFailsOnce(t *testing.T) {
	ob := newRecordingOutbox()
	ob.failErr = core.NewError(core.CodeInvalidArgument, "temporal: sink refused", nil)
	ob.failFirst = 1
	opts := testOptions(t)
	opts.Outbox = ob
	rt, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	var logged emitFailureRecord
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		err := dc.Emit(runtime.ProgressEvent{Type: "a"})
		if err != nil {
			var pe *core.Error
			if errors.As(err, &pe) {
				logged.Code = string(pe.Code)
				logged.Message = pe.Message
			}
		}
		return json.Marshal(logged)
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "e"}, RunID: "r1"}, defaultChainOpts())
	assertOutboxAppendsEqual(t, ob.callsCopy(), []outboxAppendRecord{{seq: 1, typ: "a"}})
	if types := ob.types(); len(types) != 0 {
		t.Fatalf("failed append in types: %+v", types)
	}
	var out emitFailureRecord
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out.Code != string(core.CodeInvalidArgument) || out.Message != "temporal: sink refused" {
		t.Fatalf("out %+v", out)
	}
}

func TestEmitSinkSeesTenantAndRunID(t *testing.T) {
	ob := newRecordingOutbox()
	opts := testOptions(t)
	opts.Outbox = ob
	rt, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		return nil, dc.Emit(runtime.ProgressEvent{Type: "a", Data: json.RawMessage(`"d"`)})
	})
	runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "e"}, RunID: "r1"}, defaultChainOpts())
	calls := ob.callsCopy()
	if len(calls) != 1 || calls[0].runID != "r1" || calls[0].tenant != "acme" || calls[0].seq != 1 || calls[0].typ != "a" {
		t.Fatalf("%+v", calls[0])
	}
}

func TestEmitReplayDoesNotAppendTwice(t *testing.T) {
	ob := newRecordingOutbox()
	opts := testOptions(t)
	opts.Outbox = ob
	opts.CheckpointEventThreshold = 10
	rt, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	mustRegisterStep(t, rt, "s", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"ok"`), nil
	}, runtime.StepDefaults{})
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		if err := dc.Emit(runtime.ProgressEvent{Type: "a"}); err != nil {
			return nil, err
		}
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		if err := dc.Emit(runtime.ProgressEvent{Type: "b"}); err != nil {
			return nil, err
		}
		return nil, nil
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "e"}, RunID: "r1"}, defaultChainOpts())
	if res.Execs != 2 {
		t.Fatalf("execs %d", res.Execs)
	}
	assertOutboxAppendsEqual(t, ob.callsCopy(), []outboxAppendRecord{{seq: 1, typ: "a"}, {seq: 2, typ: "b"}})
}

type recordingBlobPuts struct {
	inner platform.Blob
	mu    sync.Mutex
	puts  int
}

func (b *recordingBlobPuts) Put(ctx context.Context, key string, r io.Reader, contentType string) error {
	b.mu.Lock()
	b.puts++
	b.mu.Unlock()
	return b.inner.Put(ctx, key, r, contentType)
}

func (b *recordingBlobPuts) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return b.inner.Get(ctx, key)
}

func (b *recordingBlobPuts) Exists(ctx context.Context, key string) (bool, error) {
	return b.inner.Exists(ctx, key)
}

func (b *recordingBlobPuts) Delete(ctx context.Context, key string) error {
	return b.inner.Delete(ctx, key)
}

func (b *recordingBlobPuts) SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return b.inner.SignedURL(ctx, key, ttl)
}

func (b *recordingBlobPuts) StartUpload(ctx context.Context, o platform.UploadOptions) (platform.Upload, error) {
	return b.inner.StartUpload(ctx, o)
}

func (b *recordingBlobPuts) Commit(ctx context.Context, stagingKey string) (string, error) {
	return b.inner.Commit(ctx, stagingKey)
}

func TestEmitEventDataPassesTheCodec(t *testing.T) {
	blobs := &recordingBlobPuts{inner: testBlobStore(t)}
	opts := testOptions(t)
	opts.Blobs = blobs
	ob := newRecordingOutbox()
	opts.Outbox = ob
	rt, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	largeJSON, err := json.Marshal(strings.Repeat("x", 9000))
	if err != nil {
		t.Fatal(err)
	}
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		return nil, dc.Emit(runtime.ProgressEvent{Type: "big", Data: largeJSON})
	})
	runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "e"}, RunID: "r1"}, defaultChainOpts())
	calls := ob.callsCopy()
	if len(calls) != 1 {
		t.Fatalf("calls %d", len(calls))
	}
	if string(calls[0].data) != string(largeJSON) {
		t.Fatal("data mismatch")
	}
	blobs.mu.Lock()
	puts := blobs.puts
	blobs.mu.Unlock()
	if puts < 1 {
		t.Fatalf("blob puts %d", puts)
	}
}

func TestEmitFailureIsJournaledAndReplayedAsFailure(t *testing.T) {
	ob := newRecordingOutbox()
	ob.failFirst = 1
	ob.failErr = core.NewError(core.CodeInvalidArgument, "temporal: sink refused", nil)
	opts := testOptions(t)
	opts.Outbox = ob
	opts.CheckpointEventThreshold = 10
	rt, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	mustRegisterStep(t, rt, "s", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"ok"`), nil
	}, runtime.StepDefaults{})
	want := emitFailureRecord{Code: string(core.CodeInvalidArgument), Message: "temporal: sink refused"}
	mustRegisterFlow(t, rt, func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		var last emitFailureRecord
		if err := dc.Emit(runtime.ProgressEvent{Type: "fail"}); err != nil {
			var pe *core.Error
			if errors.As(err, &pe) {
				last = emitFailureRecord{Code: string(pe.Code), Message: pe.Message}
			}
		}
		if err := dc.Emit(runtime.ProgressEvent{Type: "ok"}); err != nil {
			return nil, err
		}
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		return json.Marshal(last)
	})
	res := runChain(t, rt, HostInput{Flow: runtime.FlowRef{Name: "e"}, RunID: "r1"}, defaultChainOpts())
	if res.Execs != 2 {
		t.Fatalf("execs %d", res.Execs)
	}
	assertOutboxAppendsEqual(t, ob.callsCopy(), []outboxAppendRecord{{seq: 1, typ: "fail"}, {seq: 2, typ: "ok"}})
	if types := ob.types(); len(types) != 1 || types[0] != "ok" {
		t.Fatalf("types %+v want only ok stored", types)
	}
	var out emitFailureRecord
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out != want {
		t.Fatalf("out %+v", out)
	}
}

func TestContinuationJSONShapeEmitFailures(t *testing.T) {
	cont := Continuation{
		Emitted: 1,
		EmitFailures: []EmitFailure{{
			Call: 2,
			Err:  JournalError{Code: string(core.CodeInvalidArgument), Message: "temporal: sink refused"},
		}},
	}
	got, err := json.Marshal(cont)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"emitted":1,"emit_failures":[{"call":2,"err":{"code":"invalid_argument","message":"temporal: sink refused"}}]}`
	if string(got) != want {
		t.Fatalf("got %s", got)
	}
}
