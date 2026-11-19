package temporal

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform/blob"
	"github.com/epzero/basamento/platform/secrets"
	"github.com/epzero/basamento/runtime"
)

func newTestRuntime(t *testing.T, checkpoint int) (*Runtime, *manualClock) {
	t.Helper()
	codec, err := NewCodec(newTestSecrets(t), newTestBlob(t), 5*1024)
	if err != nil {
		t.Fatal(err)
	}
	clock := newManualClock()
	rt, err := New(Options{
		Clock:               clock,
		IDs:                 &coretest.SeqIds{},
		Codec:               codec,
		CheckpointThreshold: checkpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt, clock
}

func registerHost(t *testing.T, rt *Runtime, fn func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error)) {
	t.Helper()
	host := func(dc runtime.DurableContext, _ runtime.FlowRef, in json.RawMessage) (json.RawMessage, error) {
		return fn(dc, in)
	}
	if err := rt.RegisterFlowHost(host); err != nil {
		t.Fatal(err)
	}
}

func registerStep(t *testing.T, rt *Runtime, name string, fn runtime.StepFunc) {
	t.Helper()
	if err := rt.RegisterStep(name, fn, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
}

func startRun(t *testing.T, rt *Runtime, tenant, id string, in json.RawMessage) runtime.RunRef {
	t.Helper()
	ref, err := rt.Start(testTenantCtx(tenant), runtime.StartRequest{
		Flow:  runtime.FlowRef{Name: "test"},
		Input: in,
		RunID: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func waitRun(t *testing.T, rt *Runtime, ref runtime.RunRef) {
	t.Helper()
	if err := rt.Wait(testTenantCtx(string(ref.TenantID)), ref); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilities(t *testing.T) {
	rt, _ := newTestRuntime(t, 0)
	caps := rt.Capabilities()
	if !caps.Durable || !caps.SyncGateUpdates || !caps.ContinueAsNew || !caps.PolyglotQueues {
		t.Fatalf("caps %+v, want every temporal trait true", caps)
	}
}

func TestStartRequiresTenant(t *testing.T) {
	rt, _ := newTestRuntime(t, 0)
	registerHost(t, rt, func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"done"`), nil
	})
	_, err := rt.Start(context.Background(), runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, RunID: "r1"})
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code %q, want invalid_argument", code)
	}
	if _, err := rt.History(testTenantCtx("acme"), runtime.RunRef{RunID: "r1"}); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("history after rejected start: %v, want not_found", err)
	}
}

func TestStartRejectsRequestTenantMismatch(t *testing.T) {
	rt, _ := newTestRuntime(t, 0)
	registerHost(t, rt, func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"done"`), nil
	})
	_, err := rt.Start(testTenantCtx("acme"), runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, RunID: "r1", TenantID: "globex"})
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code %q, want invalid_argument", code)
	}
}

func TestStepRetriesToItsAttemptLimit(t *testing.T) {
	rt, _ := newTestRuntime(t, 0)
	calls := 0
	registerStep(t, rt, "charge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		calls++
		return nil, &core.Error{Code: core.CodeVendor, Message: "vendor down", Retryable: true}
	})
	var stepErr error
	registerHost(t, rt, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		stepErr = dc.Step("charge", nil, runtime.StepOptions{MaxAttempts: 2}).Get(&out)
		return out, stepErr
	})
	ref := startRun(t, rt, "acme", "r1", nil)
	waitRun(t, rt, ref)
	if calls != 2 {
		t.Fatalf("step ran %d times, want 2", calls)
	}
	if code := core.CodeOf(stepErr); code != core.CodeVendor {
		t.Fatalf("step error %v, want vendor_error", stepErr)
	}
	status, err := rt.Describe(testTenantCtx("acme"), ref)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != stateFailed {
		t.Fatalf("state %q, want failed", status.State)
	}
}

func TestStepSucceedsOnItsLastAttempt(t *testing.T) {
	rt, _ := newTestRuntime(t, 0)
	calls := 0
	registerStep(t, rt, "charge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		calls++
		if calls < 4 {
			return nil, &core.Error{Code: core.CodeVendor, Message: "retry", Retryable: true}
		}
		return json.RawMessage(`{"ok":true}`), nil
	})
	registerHost(t, rt, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		if err := dc.Step("charge", nil, runtime.StepOptions{MaxAttempts: 4}).Get(&out); err != nil {
			return nil, err
		}
		return json.RawMessage(`"done"`), nil
	})
	ref := startRun(t, rt, "acme", "r1", nil)
	waitRun(t, rt, ref)
	if calls != 4 {
		t.Fatalf("step ran %d times, want 4", calls)
	}
	status, _ := rt.Describe(testTenantCtx("acme"), ref)
	if status.State != stateCompleted {
		t.Fatalf("state %q, want completed", status.State)
	}
}

func TestNonRetryableErrorStopsRetries(t *testing.T) {
	rt, _ := newTestRuntime(t, 0)
	calls := 0
	registerStep(t, rt, "charge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		calls++
		return nil, &core.Error{Code: core.CodeVendor, Message: "listed", Retryable: true}
	})
	registerHost(t, rt, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		return out, dc.Step("charge", nil, runtime.StepOptions{MaxAttempts: 5, NonRetryable: []string{string(core.CodeVendor)}}).Get(&out)
	})
	ref := startRun(t, rt, "acme", "r1", nil)
	waitRun(t, rt, ref)
	if calls != 1 {
		t.Fatalf("step ran %d times, want 1", calls)
	}
}

func TestStepResultRoundTripsThroughHistory(t *testing.T) {
	rt, _ := newTestRuntime(t, 0)
	big := json.RawMessage(`"` + strings.Repeat("x", 8192) + `"`)
	registerStep(t, rt, "fetch", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return big, nil
	})
	registerHost(t, rt, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		if err := dc.Step("fetch", nil, runtime.StepOptions{}).Get(&out); err != nil {
			return nil, err
		}
		return out, nil
	})
	ref := startRun(t, rt, "acme", "r1", nil)
	waitRun(t, rt, ref)
	out, err := rt.Result(testTenantCtx("acme"), ref)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, big) {
		t.Fatal("result did not round-trip")
	}
	events, err := rt.History(testTenantCtx("acme"), ref)
	if err != nil {
		t.Fatal(err)
	}
	blobs := 0
	for _, event := range events {
		if event.Payload == nil {
			continue
		}
		if event.ClaimCheck {
			blobs++
		}
		if string(event.Payload.Metadata["encoding"]) != secrets.EncodingEncrypted {
			t.Fatalf("event %s payload encoding %q, want encrypted", event.Type, event.Payload.Metadata["encoding"])
		}
	}
	if blobs != 2 {
		t.Fatalf("claim-checked payloads %d, want 2", blobs)
	}
}

// faultCodec wraps the runtime codec and alters the first encoded payload.
type faultCodec struct {
	base *Codec
	fn   func(secrets.Payload) (Encoded, bool, error)
	used bool
}

func (f *faultCodec) Encode(ctx context.Context, p secrets.Payload) (Encoded, error) {
	if !f.used {
		f.used = true
		if out, handled, err := f.fn(p); handled {
			return out, err
		}
	}
	return f.base.Encode(ctx, p)
}

func (f *faultCodec) Decode(ctx context.Context, p secrets.Payload) (secrets.Payload, error) {
	return f.base.Decode(ctx, p)
}

func TestReadableHistoryPayloadFailsTheReadingStep(t *testing.T) {
	base, err := NewCodec(newTestSecrets(t), newTestBlob(t), 5*1024)
	if err != nil {
		t.Fatal(err)
	}
	fault := &faultCodec{base: base, fn: func(p secrets.Payload) (Encoded, bool, error) {
		return Encoded{Payload: p}, true, nil
	}}
	clock := newManualClock()
	rt, err := New(Options{Clock: clock, IDs: &coretest.SeqIds{}, Codec: fault})
	if err != nil {
		t.Fatal(err)
	}
	registerStep(t, rt, "fetch", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})
	var stepErr error
	registerHost(t, rt, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		stepErr = dc.Step("fetch", nil, runtime.StepOptions{}).Get(&out)
		return out, stepErr
	})
	ref := startRun(t, rt, "acme", "r1", nil)
	waitRun(t, rt, ref)
	if code := core.CodeOf(stepErr); code != core.CodeInvalidArgument {
		t.Fatalf("step error %v, want invalid_argument", stepErr)
	}
	status, _ := rt.Describe(testTenantCtx("acme"), ref)
	if status.State != stateFailed {
		t.Fatalf("state %q, want failed", status.State)
	}
}

func TestForeignClaimCheckFailsWithNotFound(t *testing.T) {
	enc := newTestSecrets(t)
	blobs := newTestBlob(t)
	base, err := NewCodec(enc, blobs, 5*1024)
	if err != nil {
		t.Fatal(err)
	}
	foreign := bytes.Repeat([]byte("z"), 8192)
	stored, err := json.Marshal(storedPayload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: foreign})
	if err != nil {
		t.Fatal(err)
	}
	key := blob.ContentKeyOf(stored)
	globex := core.WithTenant(context.Background(), "globex")
	if err := blobs.Put(globex, key, bytes.NewReader(stored), "application/json"); err != nil {
		t.Fatal(err)
	}
	marker, _ := json.Marshal(claimCheckMarker{Key: key})
	encMarker, err := enc.Encode(testTenantCtx("acme"), secrets.Payload{Metadata: map[string][]byte{"encoding": []byte(claimCheckEncoding)}, Data: marker})
	if err != nil {
		t.Fatal(err)
	}
	fault := &faultCodec{base: base, fn: func(secrets.Payload) (Encoded, bool, error) {
		return Encoded{Payload: encMarker, ClaimCheck: true}, true, nil
	}}
	clock := newManualClock()
	rt, err := New(Options{Clock: clock, IDs: &coretest.SeqIds{}, Codec: fault})
	if err != nil {
		t.Fatal(err)
	}
	registerStep(t, rt, "fetch", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})
	var stepErr error
	registerHost(t, rt, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		stepErr = dc.Step("fetch", nil, runtime.StepOptions{}).Get(&out)
		return out, stepErr
	})
	ref := startRun(t, rt, "acme", "r1", nil)
	waitRun(t, rt, ref)
	if code := core.CodeOf(stepErr); code != core.CodeNotFound {
		t.Fatalf("step error %v, want not_found", stepErr)
	}
}

func TestCheckpointContinuesAsNewAtTheThreshold(t *testing.T) {
	rt, _ := newTestRuntime(t, 20)
	for i := 1; i <= 5; i++ {
		registerStep(t, rt, "s"+string(rune('0'+i)), func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`null`), nil
		})
	}
	registerHost(t, rt, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		for i := 1; i <= 5; i++ {
			var out json.RawMessage
			if err := dc.Step("s"+string(rune('0'+i)), nil, runtime.StepOptions{}).Get(&out); err != nil {
				return nil, err
			}
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		return json.RawMessage(`"done"`), nil
	})
	ref := startRun(t, rt, "acme", "r1", nil)
	waitRun(t, rt, ref)
	n, err := rt.ContinueAsNewCount(testTenantCtx("acme"), ref)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("continued as new %d times, want 1", n)
	}
}

func TestCheckpointBelowThresholdKeepsHistory(t *testing.T) {
	rt, _ := newTestRuntime(t, 1000)
	registerStep(t, rt, "s1", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`null`), nil
	})
	registerHost(t, rt, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		if err := dc.Step("s1", nil, runtime.StepOptions{}).Get(&out); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		return json.RawMessage(`"done"`), nil
	})
	ref := startRun(t, rt, "acme", "r1", nil)
	waitRun(t, rt, ref)
	n, _ := rt.ContinueAsNewCount(testTenantCtx("acme"), ref)
	if n != 0 {
		t.Fatalf("continued as new %d times, want 0", n)
	}
}

func TestCheckpointDrainsInFlightSteps(t *testing.T) {
	rt, _ := newTestRuntime(t, 20)
	release := make(chan struct{})
	started := make(chan string, 2)
	step := func(name string) runtime.StepFunc {
		return func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			started <- name
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return json.RawMessage(`null`), nil
		}
	}
	for i := 1; i <= 5; i++ {
		registerStep(t, rt, "s"+string(rune('0'+i)), func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`null`), nil
		})
	}
	registerStep(t, rt, "a", step("a"))
	registerStep(t, rt, "b", step("b"))
	registerHost(t, rt, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		for i := 1; i <= 5; i++ {
			var out json.RawMessage
			if err := dc.Step("s"+string(rune('0'+i)), nil, runtime.StepOptions{}).Get(&out); err != nil {
				return nil, err
			}
		}
		fa := dc.Step("a", nil, runtime.StepOptions{})
		fb := dc.Step("b", nil, runtime.StepOptions{})
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		var out json.RawMessage
		if err := fa.Get(&out); err != nil {
			return nil, err
		}
		if err := fb.Get(&out); err != nil {
			return nil, err
		}
		return json.RawMessage(`"done"`), nil
	})
	ref := startRun(t, rt, "acme", "r1", nil)
	<-started
	<-started
	close(release)
	waitRun(t, rt, ref)
	n, _ := rt.ContinueAsNewCount(testTenantCtx("acme"), ref)
	if n != 1 {
		t.Fatalf("continued as new %d times, want 1", n)
	}
	events, _ := rt.History(testTenantCtx("acme"), ref)
	continued, completedB := -1, -1
	for i, event := range events {
		if event.Type == eventContinuedAsNew {
			continued = i
		}
		if event.Type == eventActivityCompleted && event.Name == "b" {
			completedB = i
		}
	}
	if completedB < 0 || continued < 0 || completedB > continued {
		t.Fatalf("b completed at %d, continue-as-new at %d; want b first", completedB, continued)
	}
}

func TestGateResolveRoundTrips(t *testing.T) {
	rt, _ := newTestRuntime(t, 0)
	registerHost(t, rt, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		if err := dc.Await(runtime.GateSpec{Name: "approve"}).Get(&out); err != nil {
			return nil, err
		}
		return out, nil
	})
	ref := startRun(t, rt, "acme", "r1", nil)
	if err := rt.WaitForGate(testTenantCtx("acme"), ref, "approve"); err != nil {
		t.Fatal(err)
	}
	if err := rt.Resolve(testTenantCtx("acme"), ref, "approve", json.RawMessage(`{"approved":true}`)); err != nil {
		t.Fatal(err)
	}
	waitRun(t, rt, ref)
	out, err := rt.Result(testTenantCtx("acme"), ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"approved":true}` {
		t.Fatalf("result %s", out)
	}
}

func TestTenantIsolation(t *testing.T) {
	rt, _ := newTestRuntime(t, 0)
	registerHost(t, rt, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		if err := dc.Await(runtime.GateSpec{Name: "approve"}).Get(&out); err != nil {
			return nil, err
		}
		return out, nil
	})
	ref := startRun(t, rt, "acme", "r1", nil)
	if err := rt.WaitForGate(testTenantCtx("acme"), ref, "approve"); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Describe(testTenantCtx("globex"), runtime.RunRef{RunID: ref.RunID}); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("cross-tenant describe: %v, want not_found", err)
	}
	if _, err := rt.Describe(testTenantCtx("globex"), ref); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("cross-tenant ref: %v, want invalid_argument", err)
	}
	list, _, err := rt.List(testTenantCtx("globex"), runtime.RunQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("globex listed %d acme runs", len(list))
	}
	if err := rt.Cancel(testTenantCtx("acme"), ref, "test"); err != nil {
		t.Fatal(err)
	}
}

func TestVersionPin(t *testing.T) {
	for _, version := range []string{"1.31.9", "1.33.0"} {
		base, _ := NewCodec(newTestSecrets(t), newTestBlob(t), 0)
		rt, err := New(Options{Clock: newManualClock(), IDs: &coretest.SeqIds{}, Codec: base, ServerVersion: version})
		if err != nil {
			t.Fatal(err)
		}
		err = rt.Serve(context.Background())
		if code := core.CodeOf(err); code != core.CodeUnsupported {
			t.Fatalf("version %s: %v, want unsupported", version, err)
		}
	}
	base, _ := NewCodec(newTestSecrets(t), newTestBlob(t), 0)
	rt, err := New(Options{Clock: newManualClock(), IDs: &coretest.SeqIds{}, Codec: base})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rt.Serve(ctx) }()
	<-rt.Started()
	if !rt.Serving() {
		t.Fatal("runtime did not report serving")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReplayIsDeterministic(t *testing.T) {
	rt, _ := newTestRuntime(t, 0)
	registerStep(t, rt, "s1", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"n":1}`), nil
	})
	registerHost(t, rt, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		if err := dc.Step("s1", nil, runtime.StepOptions{}).Get(&out); err != nil {
			return nil, err
		}
		if err := dc.Emit(runtime.ProgressEvent{Type: "progress", Data: out}); err != nil {
			return nil, err
		}
		return out, nil
	})
	ref := startRun(t, rt, "acme", "r1", nil)
	waitRun(t, rt, ref)
	if err := rt.Replay(testTenantCtx("acme"), ref); err != nil {
		t.Fatalf("replay: %v", err)
	}
	target := rt.lookup("acme", "r1")
	target.mu.Lock()
	target.calls[0] = "step:other"
	target.mu.Unlock()
	if err := rt.Replay(testTenantCtx("acme"), ref); core.CodeOf(err) != core.CodeDeterminismBroken {
		t.Fatalf("mutated replay: %v, want determinism_violation", err)
	}
}

func TestTimerWaitsForTheClock(t *testing.T) {
	rt, clock := newTestRuntime(t, 0)
	timerStarted := make(chan struct{})
	registerHost(t, rt, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		future := dc.Sleep(10 * time.Second)
		close(timerStarted)
		if err := future.Get(nil); err != nil {
			return nil, err
		}
		return json.RawMessage(`"done"`), nil
	})
	ref := startRun(t, rt, "acme", "r1", nil)
	<-timerStarted
	status, _ := rt.Describe(testTenantCtx("acme"), ref)
	if status.State != stateRunning {
		t.Fatalf("run ended before the timer: %q", status.State)
	}
	clock.Advance(9 * time.Second)
	status, _ = rt.Describe(testTenantCtx("acme"), ref)
	if status.State != stateRunning {
		t.Fatalf("run ended 1s early: %q", status.State)
	}
	clock.Advance(time.Second)
	waitRun(t, rt, ref)
	out, err := rt.Result(testTenantCtx("acme"), ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `"done"` {
		t.Fatalf("result %s", out)
	}
}

func TestSignalReachesGateAndQueryReadsState(t *testing.T) {
	rt, _ := newTestRuntime(t, 0)
	registerHost(t, rt, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		if err := dc.Await(runtime.GateSpec{Name: "go"}).Get(&out); err != nil {
			return nil, err
		}
		return out, nil
	})
	ref := startRun(t, rt, "acme", "r1", nil)
	if err := rt.WaitForGate(testTenantCtx("acme"), ref, "go"); err != nil {
		t.Fatal(err)
	}
	if err := rt.Signal(testTenantCtx("acme"), ref, "go", json.RawMessage(`{"go":true}`)); err != nil {
		t.Fatal(err)
	}
	waitRun(t, rt, ref)
	got, err := rt.Query(testTenantCtx("acme"), ref, "status")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `"completed"` {
		t.Fatalf("query status %s", got)
	}
}

func TestChildRunCompletes(t *testing.T) {
	rt, _ := newTestRuntime(t, 0)
	registerHost(t, rt, func(dc runtime.DurableContext, input json.RawMessage) (json.RawMessage, error) {
		if string(input) == `"child"` {
			return json.RawMessage(`{"child":true}`), nil
		}
		var out json.RawMessage
		if err := dc.Child(runtime.FlowRef{Name: "child"}, json.RawMessage(`"child"`), runtime.ChildOptions{ID: "c1"}).Get(&out); err != nil {
			return nil, err
		}
		return out, nil
	})
	ref := startRun(t, rt, "acme", "r1", nil)
	waitRun(t, rt, ref)
	out, err := rt.Result(testTenantCtx("acme"), ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"child":true}` {
		t.Fatalf("result %s", out)
	}
}

func TestUnknownStepFailsTheRun(t *testing.T) {
	rt, _ := newTestRuntime(t, 0)
	registerHost(t, rt, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		return out, dc.Step("missing", nil, runtime.StepOptions{}).Get(&out)
	})
	ref := startRun(t, rt, "acme", "r1", nil)
	waitRun(t, rt, ref)
	if _, err := rt.Result(testTenantCtx("acme"), ref); core.CodeOf(err) != core.CodeUnsupported {
		t.Fatalf("result error %v, want unsupported", err)
	}
}
