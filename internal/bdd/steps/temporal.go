package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform/blob"
	"github.com/epzero/basamento/platform/secrets"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/runtime/temporal"
)

// temporalWorld drives runtime/temporal against a deterministic client-side
// simulator of the Temporal server: a fake clock, a simulated key service and
// injected activity results. It records what the scenarios observe — history
// payloads, continue-as-new events and step executions — without a live server,
// sleeps or the wall clock.
type temporalWorld struct {
	mu sync.Mutex

	rt     *temporal.Runtime
	blobs  *blob.Store
	blobAt string
	enc    *secrets.Codec
	codec  *temporal.Codec
	fault  *temporalFaultCodec
	sim    *simKeyRegistry

	version    string
	threshold  int
	checkpoint int
	attempts   int

	flows      map[string]func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error)
	behaviors  map[string]*temporalStepBehavior
	registered map[string]bool

	faultMode string
	faultKey  string

	lastRef     runtime.RunRef
	lastErr     error
	lastStepErr error
	lastRunID   string
	serveCancel context.CancelFunc
}

type temporalStepBehavior struct {
	code      core.Code
	retryable bool
	failFirst int // -1 fails every attempt
	calls     int
	output    json.RawMessage
}

// temporalFaultCodec wraps the composed codec. A scenario can make the first
// encoded payload readable (RT-032) or a foreign claim-check marker (RT-033).
type temporalFaultCodec struct {
	base *temporal.Codec
	enc  *secrets.Codec
	mode string
	key  string
	used bool
}

func (f *temporalFaultCodec) Encode(ctx context.Context, p secrets.Payload) (temporal.Encoded, error) {
	if f.mode != "" && !f.used {
		f.used = true
		switch f.mode {
		case "plain":
			return temporal.Encoded{Payload: p}, nil
		case "foreign":
			marker, _ := json.Marshal(map[string]string{"key": f.key})
			encMarker, err := f.enc.Encode(ctx, secrets.Payload{
				Metadata: map[string][]byte{"encoding": []byte("basamento/claim-check")},
				Data:     marker,
			})
			if err != nil {
				return temporal.Encoded{}, err
			}
			return temporal.Encoded{Payload: encMarker, ClaimCheck: true}, nil
		}
	}
	return f.base.Encode(ctx, p)
}

func (f *temporalFaultCodec) Decode(ctx context.Context, p secrets.Payload) (secrets.Payload, error) {
	return f.base.Decode(ctx, p)
}

func newTemporalWorld() *temporalWorld {
	return &temporalWorld{
		version:    temporal.PinnedServerVersion,
		flows:      map[string]func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error){},
		behaviors:  map[string]*temporalStepBehavior{},
		registered: map[string]bool{},
	}
}

func temporalW(ctx context.Context) *temporalWorld { return world(ctx).Temporal }

func (w *temporalWorld) close() {
	if w.serveCancel != nil {
		w.serveCancel()
	}
	if w.blobs != nil {
		_ = w.blobs.Close()
	}
	if w.blobAt != "" {
		_ = os.RemoveAll(w.blobAt)
	}
}

// prepare builds the blob store and encryption codec the Background names,
// without fixing the runtime's settings yet: the scenario's Given steps still
// choose the claim-check and checkpoint thresholds and the server version.
func (w *temporalWorld) prepareLocked() error {
	if w.blobs != nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "basamento-temporal-bdd-")
	if err != nil {
		return err
	}
	w.blobAt = dir
	st, _, err := blob.Open(context.Background(), "file://"+dir, blob.Options{})
	if err != nil {
		return err
	}
	w.blobs = st
	w.sim = newSimRegistry()
	w.sim.hold("mk1")
	key := &simKeyService{reg: w.sim, name: "mk1"}
	enc, err := secrets.NewCodec(newBDDKeysetStore(), []secrets.KeyService{key})
	if err != nil {
		return err
	}
	w.enc = enc
	return nil
}

func (w *temporalWorld) prepare() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.prepareLocked()
}

// ensureRuntime builds the runtime on first use, after the scenario has set the
// server version, thresholds and fault injection it needs.
func (w *temporalWorld) ensureRuntime() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.rt != nil {
		return nil
	}
	if err := w.prepareLocked(); err != nil {
		return err
	}
	codec, err := temporal.NewCodec(w.enc, w.blobs, w.threshold)
	if err != nil {
		return err
	}
	w.codec = codec
	w.fault = &temporalFaultCodec{base: codec, enc: w.enc, mode: w.faultMode, key: w.faultKey}
	rt, err := temporal.New(temporal.Options{
		Clock:               coretest.NewFakeClock(time.Unix(1_700_000_000, 0).UTC()),
		IDs:                 &coretest.SeqIds{},
		Codec:               w.fault,
		CheckpointThreshold: w.checkpoint,
		ServerVersion:       w.version,
	})
	if err != nil {
		return err
	}
	if err := rt.RegisterFlowHost(w.runFlow); err != nil {
		return err
	}
	w.rt = rt
	for name := range w.behaviors {
		if err := w.registerStepLocked(name); err != nil {
			return err
		}
	}
	return nil
}

func (w *temporalWorld) runFlow(dc runtime.DurableContext, ref runtime.FlowRef, in json.RawMessage) (json.RawMessage, error) {
	w.mu.Lock()
	fn := w.flows[ref.Name]
	w.mu.Unlock()
	if fn == nil {
		return nil, core.NewError(core.CodeNotFound, fmt.Sprintf("temporal bdd: flow %q is not declared", ref.Name), map[string]any{"flow": ref.Name})
	}
	return fn(dc, in)
}

func (w *temporalWorld) addFlow(name string, fn func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error)) error {
	if err := w.ensureRuntime(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flows[name] = fn
	return nil
}

func (w *temporalWorld) registerStepLocked(name string) error {
	if w.registered[name] {
		return nil
	}
	w.registered[name] = true
	return w.rt.RegisterStep(name, func(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
		return w.callStep(ctx, name, in)
	}, runtime.StepDefaults{})
}

func (w *temporalWorld) ensureStep(name string) error {
	if err := w.ensureRuntime(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.behaviors[name]; !ok {
		w.behaviors[name] = &temporalStepBehavior{output: json.RawMessage(`null`)}
	}
	return w.registerStepLocked(name)
}

func (w *temporalWorld) callStep(ctx context.Context, name string, _ json.RawMessage) (json.RawMessage, error) {
	w.mu.Lock()
	b := w.behaviors[name]
	if b == nil {
		b = &temporalStepBehavior{output: json.RawMessage(`null`)}
		w.behaviors[name] = b
	}
	b.calls++
	shouldFail := b.failFirst < 0 || b.calls <= b.failFirst
	output := b.output
	code, retryable := b.code, b.retryable
	w.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, core.NewError(core.CodeCancelled, "temporal bdd: step cancelled", nil)
	}
	if shouldFail && code != "" {
		return nil, &core.Error{Code: code, Message: "temporal bdd: step failed", Retryable: retryable}
	}
	return append(json.RawMessage(nil), output...), nil
}

func (w *temporalWorld) stepCalls(name string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if b := w.behaviors[name]; b != nil {
		return b.calls
	}
	return 0
}

func (w *temporalWorld) start(ctx context.Context, tenant, flowName, runID string, requestTenant core.TenantID) (runtime.RunRef, error) {
	if err := w.ensureRuntime(); err != nil {
		return runtime.RunRef{}, err
	}
	if err := w.ensureNamedFlow(flowName); err != nil {
		return runtime.RunRef{}, err
	}
	if tenant != "" {
		ctx = core.WithTenant(ctx, core.TenantID(tenant))
	}
	ref, err := w.rt.Start(ctx, runtime.StartRequest{
		Flow:     runtime.FlowRef{Name: flowName},
		RunID:    runID,
		TenantID: requestTenant,
	})
	w.mu.Lock()
	w.lastRef = ref
	w.lastErr = err
	w.lastRunID = runID
	w.mu.Unlock()
	return ref, err
}

func (w *temporalWorld) wait(tenant string, ref runtime.RunRef) error {
	ctx := core.WithTenant(context.Background(), core.TenantID(tenant))
	return w.rt.Wait(ctx, ref)
}

func (w *temporalWorld) state(tenant string, ref runtime.RunRef) (string, error) {
	ctx := core.WithTenant(context.Background(), core.TenantID(tenant))
	status, err := w.rt.Describe(ctx, ref)
	return status.State, err
}

func (w *temporalWorld) result(tenant string, ref runtime.RunRef) (json.RawMessage, error) {
	ctx := core.WithTenant(context.Background(), core.TenantID(tenant))
	return w.rt.Result(ctx, ref)
}

func (w *temporalWorld) history(tenant string, ref runtime.RunRef) ([]temporal.HistoryEvent, error) {
	ctx := core.WithTenant(context.Background(), core.TenantID(tenant))
	return w.rt.History(ctx, ref)
}

// ensureNamedFlow builds the flows the scenarios start without declaring one.
func (w *temporalWorld) ensureNamedFlow(name string) error {
	w.mu.Lock()
	_, ok := w.flows[name]
	w.mu.Unlock()
	if ok {
		return nil
	}
	switch name {
	case "pay":
		return w.addFlow("pay", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
			var out json.RawMessage
			err := dc.Step("charge", nil, runtime.StepOptions{MaxAttempts: w.attempts}).Get(&out)
			w.mu.Lock()
			w.lastStepErr = err
			w.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return out, nil
		})
	case "fetch", "render":
		if err := w.ensureStep(name); err != nil {
			return err
		}
		return w.addFlow(name, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
			var out json.RawMessage
			err := dc.Step(name, nil, runtime.StepOptions{}).Get(&out)
			w.mu.Lock()
			w.lastStepErr = err
			w.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return out, nil
		})
	case "review":
		return w.addFlow("review", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
			var out json.RawMessage
			if err := dc.Await(runtime.GateSpec{Name: "approve"}).Get(&out); err != nil {
				return nil, err
			}
			return out, nil
		})
	}
	return nil
}

func (w *temporalWorld) addStepFlow(name, step string, output json.RawMessage, returnIt bool, literal json.RawMessage) error {
	if err := w.ensureStep(step); err != nil {
		return err
	}
	w.mu.Lock()
	w.behaviors[step].output = append(json.RawMessage(nil), output...)
	w.mu.Unlock()
	return w.addFlow(name, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var got json.RawMessage
		err := dc.Step(step, nil, runtime.StepOptions{}).Get(&got)
		w.mu.Lock()
		w.lastStepErr = err
		w.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if returnIt {
			return got, nil
		}
		return append(json.RawMessage(nil), literal...), nil
	})
}

func (w *temporalWorld) addManySteps(name, prefix string, count int, checkpointEach bool, gate bool, literal json.RawMessage) error {
	for i := 1; i <= count; i++ {
		step := fmt.Sprintf("%s%d", prefix, i)
		if err := w.ensureStep(step); err != nil {
			return err
		}
	}
	return w.addFlow(name, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		for i := 1; i <= count; i++ {
			var out json.RawMessage
			if err := dc.Step(fmt.Sprintf("%s%d", prefix, i), nil, runtime.StepOptions{}).Get(&out); err != nil {
				return nil, err
			}
			if checkpointEach {
				if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
					return nil, err
				}
			}
		}
		if !checkpointEach {
			if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
				return nil, err
			}
		}
		if gate {
			var out json.RawMessage
			if err := dc.Await(runtime.GateSpec{Name: "approve"}).Get(&out); err != nil {
				return nil, err
			}
			if literal == nil {
				return out, nil
			}
		}
		return append(json.RawMessage(nil), literal...), nil
	})
}

func (w *temporalWorld) addEmitFlow() error {
	for i := 1; i <= 40; i++ {
		if err := w.ensureStep(fmt.Sprintf("s%d", i)); err != nil {
			return err
		}
	}
	for i := 1; i <= 5; i++ {
		if err := w.ensureStep(fmt.Sprintf("t%d", i)); err != nil {
			return err
		}
	}
	return w.addFlow("emit", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		for _, letter := range []string{"a", "b", "c", "d", "e", "f"} {
			if err := dc.Emit(runtime.ProgressEvent{Type: letter, Data: json.RawMessage(`"` + letter + `"`)}); err != nil {
				return nil, err
			}
		}
		for i := 1; i <= 40; i++ {
			var out json.RawMessage
			if err := dc.Step(fmt.Sprintf("s%d", i), nil, runtime.StepOptions{}).Get(&out); err != nil {
				return nil, err
			}
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); err != nil {
			return nil, err
		}
		for i := 1; i <= 5; i++ {
			var out json.RawMessage
			if err := dc.Step(fmt.Sprintf("t%d", i), nil, runtime.StepOptions{}).Get(&out); err != nil {
				return nil, err
			}
		}
		return json.RawMessage(`"done"`), nil
	})
}

func (w *temporalWorld) addParallelFlow() error {
	for i := 1; i <= 5; i++ {
		if err := w.ensureStep(fmt.Sprintf("s%d", i)); err != nil {
			return err
		}
	}
	for _, name := range []string{"a", "b"} {
		if err := w.ensureStep(name); err != nil {
			return err
		}
	}
	return w.addFlow("parallel", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		for i := 1; i <= 5; i++ {
			var out json.RawMessage
			if err := dc.Step(fmt.Sprintf("s%d", i), nil, runtime.StepOptions{}).Get(&out); err != nil {
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
}

func seedJSON(size, seed int) json.RawMessage {
	if size < 2 {
		size = 2
	}
	b := make([]byte, size-2)
	x := uint64(seed)*6364136223846793005 + 1442695040888963407
	for i := range b {
		x = x*6364136223846793005 + 1442695040888963407
		b[i] = byte('a' + (x>>33)%26)
	}
	return json.RawMessage(`"` + string(b) + `"`)
}

func seedBytes(size, seed int) []byte {
	b := make([]byte, size)
	x := uint64(seed)*2862933555777941757 + 3037000493
	for i := range b {
		x = x*6364136223846793005 + 1442695040888963407
		b[i] = byte(x >> 33)
	}
	return b
}

func parseTenant(s string) core.TenantID { return core.TenantID(s) }

func optionalTenant(s string) core.TenantID {
	if s == "" {
		return ""
	}
	return parseTenant(s)
}

func registerTemporal(sc *godog.ScenarioContext) {
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w := world(ctx).Temporal; w != nil {
			w.close()
		}
		return ctx, nil
	})

	sc.Step(`^a Temporal test server at version "([^"]*)"$`, func(ctx context.Context, version string) error {
		temporalW(ctx).version = version
		return nil
	})
	sc.Step(`^a blob store and a payload codec served by a simulated key service$`, func(ctx context.Context) error {
		return temporalW(ctx).prepare()
	})
	sc.Step(`^the runtime is "temporal"$`, func(ctx context.Context) error {
		conformanceW(ctx).runtimeName = "temporal"
		return temporalW(ctx).ensureRuntime()
	})

	// Step behavior.
	sc.Step(`^step "([^"]*)" fails with code "([^"]*)" marked retryable (true|false) on its first (\d+) executions$`, func(ctx context.Context, name, code, retryable string, times int) error {
		w := temporalW(ctx)
		if err := w.ensureStep(name); err != nil {
			return err
		}
		w.mu.Lock()
		b := w.behaviors[name]
		b.code = core.Code(code)
		b.retryable = retryable == "true"
		b.failFirst = times
		w.mu.Unlock()
		return nil
	})
	sc.Step(`^step "([^"]*)" fails with code "([^"]*)"$`, func(ctx context.Context, name, code string) error {
		return temporalStepFailure(temporalW(ctx), code)
	})

	// Running.
	sc.Step(`^a run of "([^"]*)" on the temporal runtime runs for 1 day of test time$`, func(ctx context.Context, flow string) error {
		w := temporalW(ctx)
		ref, err := w.start(context.Background(), "acme", flow, "r1", "")
		if err != nil {
			return err
		}
		return w.wait("acme", ref)
	})
	sc.Step(`^a run of "([^"]*)" on the temporal runtime completes$`, func(ctx context.Context, flow string) error {
		w := temporalW(ctx)
		ref, err := w.start(context.Background(), "acme", flow, "r1", "")
		if err != nil {
			return err
		}
		return w.wait("acme", ref)
	})
	sc.Step(`^a run of "([^"]*)" for tenant "([^"]*)" on the temporal runtime completes$`, func(ctx context.Context, flow, tenant string) error {
		w := temporalW(ctx)
		ref, err := w.start(context.Background(), tenant, flow, "r1", "")
		if err != nil {
			return err
		}
		return w.wait(tenant, ref)
	})

	// Flows that call one step.
	sc.Step(`^a flow calls step "([^"]*)" whose result is (\d+) bytes of seed (\d+) and returns "([^"]*)"$`, func(ctx context.Context, step string, size, seed int, literal string) error {
		return temporalW(ctx).addStepFlow("fetch", step, seedJSON(size, seed), false, json.RawMessage(`"`+literal+`"`))
	})
	sc.Step(`^a flow calls step "([^"]*)" whose result is (\d+) bytes of seed (\d+) and returns it$`, func(ctx context.Context, step string, size, seed int) error {
		return temporalW(ctx).addStepFlow("fetch", step, seedJSON(size, seed), true, nil)
	})
	sc.Step(`^a flow calls step "([^"]*)" whose result is (\{.*\}) and returns it$`, func(ctx context.Context, step, result string) error {
		return temporalW(ctx).addStepFlow("fetch", step, json.RawMessage(result), true, nil)
	})
	sc.Step(`^a flow calls step "([^"]*)" that returns (\{.*\}) and returns it$`, func(ctx context.Context, step, result string) error {
		return temporalW(ctx).addStepFlow("fetch", step, json.RawMessage(result), true, nil)
	})
	sc.Step(`^a flow calls step "([^"]*)" that returns (\{.*\}) and returns "([^"]*)"$`, func(ctx context.Context, step, result, literal string) error {
		return temporalW(ctx).addStepFlow("fetch", step, json.RawMessage(result), false, json.RawMessage(`"`+literal+`"`))
	})

	// Claim-check history.
	sc.Step(`^the payload codec claim-check threshold is (\d+) KB$`, func(ctx context.Context, kb int) error {
		temporalW(ctx).threshold = kb * 1024
		return nil
	})
	sc.Step(`^the run history holds (\d+) claim-checked payloads$`, func(ctx context.Context, want int) error {
		w := temporalW(ctx)
		events, err := w.history(string(w.lastRef.TenantID), w.lastRef)
		if err != nil {
			return err
		}
		got := 0
		for _, event := range events {
			if event.ClaimCheck {
				got++
			}
		}
		if got != want {
			return fmt.Errorf("run history holds %d claim-checked payloads, want %d", got, want)
		}
		return nil
	})
	sc.Step(`^every payload in the run history is at most 5 KB$`, func(ctx context.Context) error {
		w := temporalW(ctx)
		events, err := w.history(string(w.lastRef.TenantID), w.lastRef)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.Payload != nil && len(event.Payload.Data) > 5*1024 {
				return fmt.Errorf("event %s payload is %d bytes, want at most 5 KB", event.Type, len(event.Payload.Data))
			}
		}
		return nil
	})
	sc.Step(`^the run history holds at least (\d+) payload, and every payload has encoding "([^"]*)"$`, func(ctx context.Context, least int, encoding string) error {
		w := temporalW(ctx)
		events, err := w.history(string(w.lastRef.TenantID), w.lastRef)
		if err != nil {
			return err
		}
		n := 0
		for _, event := range events {
			if event.Payload == nil {
				continue
			}
			n++
			if got := string(event.Payload.Metadata["encoding"]); got != encoding {
				return fmt.Errorf("event %s payload encoding %q, want %q", event.Type, got, encoding)
			}
		}
		if n < least {
			return fmt.Errorf("run history holds %d payloads, want at least %d", n, least)
		}
		return nil
	})
	sc.Step(`^the run history holds neither "([^"]*)" nor "([^"]*)"$`, func(ctx context.Context, first, second string) error {
		w := temporalW(ctx)
		events, err := w.history(string(w.lastRef.TenantID), w.lastRef)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.Payload == nil {
				continue
			}
			if bytes.Contains(event.Payload.Data, []byte(first)) {
				return fmt.Errorf("event %s payload holds %q", event.Type, first)
			}
			if string(event.Payload.Metadata["encoding"]) == second {
				return fmt.Errorf("event %s payload has encoding %q", event.Type, second)
			}
		}
		return nil
	})

	// No tenant and mismatched tenants.
	sc.Step(`^a run of "([^"]*)" without a tenant is started on the temporal runtime$`, func(ctx context.Context, flow string) error {
		w := temporalW(ctx)
		_, _ = w.start(context.Background(), "", flow, "r1", "")
		return nil
	})
	sc.Step(`^the Temporal test server holds no run history for that start$`, func(ctx context.Context) error {
		w := temporalW(ctx)
		_, err := w.history("acme", runtime.RunRef{RunID: w.lastRunID})
		if core.CodeOf(err) != core.CodeNotFound {
			return fmt.Errorf("history for rejected start: %w, want not_found", err)
		}
		return nil
	})

	// Unencrypted and foreign payloads.
	sc.Step(`^step "([^"]*)" recorded an unencrypted payload in the run history of a run on the temporal runtime$`, func(ctx context.Context, step string) error {
		w := temporalW(ctx)
		w.faultMode = "plain"
		if err := w.addStepFlow("fetch", step, json.RawMessage(`{"ok":true}`), true, nil); err != nil {
			return err
		}
		ref, err := w.start(context.Background(), "acme", "fetch", "r1", "")
		if err != nil {
			return err
		}
		w.lastRef = ref
		return w.wait("acme", ref)
	})
	sc.Step(`^tenant "([^"]*)" has stored a blob of (\d+) bytes of seed (\d+)$`, func(ctx context.Context, tenant string, size, seed int) error {
		w := temporalW(ctx)
		if err := w.ensureRuntime(); err != nil {
			return err
		}
		content := seedBytes(size, seed)
		key := blob.ContentKeyOf(content)
		ctx = core.WithTenant(context.Background(), core.TenantID(tenant))
		if err := w.blobs.Put(ctx, key, bytes.NewReader(content), "application/octet-stream"); err != nil {
			return err
		}
		if err := w.ensureStep("fetch"); err != nil {
			return err
		}
		w.mu.Lock()
		w.behaviors["fetch"].output = json.RawMessage(`{"ok":true}`)
		w.mu.Unlock()
		w.faultMode = "foreign"
		w.faultKey = key
		if w.fault != nil {
			w.fault.mode = "foreign"
			w.fault.key = key
		}
		return nil
	})
	sc.Step(`^a run on the temporal runtime for tenant "([^"]*)" has a step "([^"]*)" result in its run history naming that blob's content key$`, func(ctx context.Context, tenant, step string) error {
		w := temporalW(ctx)
		if err := w.addStepFlow("fetch", step, json.RawMessage(`{"ok":true}`), true, nil); err != nil {
			return err
		}
		ref, err := w.start(context.Background(), tenant, "fetch", "r1", "")
		if err != nil {
			return err
		}
		w.lastRef = ref
		return w.wait(tenant, ref)
	})
	sc.Step(`^the run reads the result of step "([^"]*)"$`, func(ctx context.Context, step string) error {
		w := temporalW(ctx)
		return w.wait(string(w.lastRef.TenantID), w.lastRef)
	})

	// ContinueAsNew.
	sc.Step(`^the checkpoint event threshold is (\d+)$`, func(ctx context.Context, n int) error {
		temporalW(ctx).checkpoint = n
		return nil
	})
	sc.Step(`^a flow calls (\d+) steps "([^"]*)" to "([^"]*)" and checkpoints after each, then returns "([^"]*)"$`, func(ctx context.Context, count int, first, last, literal string) error {
		return temporalW(ctx).addManySteps("steps", strings.TrimRight(first, "0123456789"), count, true, false, json.RawMessage(`"`+literal+`"`))
	})
	sc.Step(`^a flow calls (\d+) steps "([^"]*)" to "([^"]*)", checkpoints, then returns "([^"]*)"$`, func(ctx context.Context, count int, first, last, literal string) error {
		return temporalW(ctx).addManySteps("steps", strings.TrimRight(first, "0123456789"), count, false, false, json.RawMessage(`"`+literal+`"`))
	})
	sc.Step(`^a flow calls (\d+) steps "([^"]*)" to "([^"]*)", checkpoints, then waits at gate "([^"]*)"$`, func(ctx context.Context, count int, first, last, gate string) error {
		return temporalW(ctx).addManySteps("short", strings.TrimRight(first, "0123456789"), count, false, true, nil)
	})
	sc.Step(`^a flow calls (\d+) steps "([^"]*)" to "([^"]*)", checkpoints, then waits at gate "([^"]*)" and returns (\{.*\})$`, func(ctx context.Context, count int, first, last, gate, literal string) error {
		return temporalW(ctx).addManySteps("steps", strings.TrimRight(first, "0123456789"), count, false, true, json.RawMessage(literal))
	})
	sc.Step(`^a run of "([^"]*)" on the temporal runtime has started and waits at gate "([^"]*)"$`, func(ctx context.Context, flow, gate string) error {
		w := temporalW(ctx)
		ref, err := w.start(context.Background(), "acme", flow, "r1", "")
		if err != nil {
			return err
		}
		return w.rt.WaitForGate(core.WithTenant(context.Background(), "acme"), ref, gate)
	})
	sc.Step(`^run "([^"]*)" of that flow on the temporal runtime is waiting at gate "([^"]*)" after continuing as new$`, func(ctx context.Context, runID, gate string) error {
		w := temporalW(ctx)
		ref, err := w.start(context.Background(), "acme", "steps", runID, "")
		if err != nil {
			return err
		}
		if err := w.rt.WaitForGate(core.WithTenant(context.Background(), "acme"), ref, gate); err != nil {
			return err
		}
		n, err := w.rt.ContinueAsNewCount(core.WithTenant(context.Background(), "acme"), ref)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("run did not continue as new before the gate")
		}
		return nil
	})
	sc.Step(`^gate "([^"]*)" is resolved with payload (\{.*\})$`, func(ctx context.Context, gate, payload string) error {
		w := temporalW(ctx)
		// Resolve hands the payload to the run; the run consumes it on its own
		// goroutine. Wait for it to settle so the next assertion sees the
		// post-resolution state instead of racing it.
		callCtx := core.WithTenant(context.Background(), w.lastRef.TenantID)
		if err := w.rt.Resolve(callCtx, w.lastRef, gate, json.RawMessage(payload)); err != nil {
			return err
		}
		return w.rt.Wait(callCtx, w.lastRef)
	})
	sc.Step(`^gate "([^"]*)" on run "([^"]*)" is resolved with payload (\{.*\})$`, func(ctx context.Context, gate, runID, payload string) error {
		w := temporalW(ctx)
		ref := runtime.RunRef{RunID: runID}
		callCtx := core.WithTenant(context.Background(), "acme")
		if err := w.rt.Resolve(callCtx, ref, gate, json.RawMessage(payload)); err != nil {
			return err
		}
		return w.rt.Wait(callCtx, ref)
	})
	sc.Step(`^the run was continued as new at least once$`, func(ctx context.Context) error {
		w := temporalW(ctx)
		n, err := w.rt.ContinueAsNewCount(core.WithTenant(context.Background(), w.lastRef.TenantID), w.lastRef)
		if err != nil {
			return err
		}
		if n < 1 {
			return fmt.Errorf("run continued as new %d times, want at least 1", n)
		}
		return nil
	})
	sc.Step(`^the run was continued as new (\d+) times?$`, func(ctx context.Context, want int) error {
		w := temporalW(ctx)
		n, err := w.rt.ContinueAsNewCount(core.WithTenant(context.Background(), w.lastRef.TenantID), w.lastRef)
		if err != nil {
			return err
		}
		if n != want {
			return fmt.Errorf("run continued as new %d times, want %d", n, want)
		}
		return nil
	})
	sc.Step(`^steps "([^"]*)" to "([^"]*)" were executed once each$`, func(ctx context.Context, first, last string) error {
		w := temporalW(ctx)
		var start, end int
		if _, err := fmt.Sscanf(first, "s%d", &start); err != nil {
			return err
		}
		if _, err := fmt.Sscanf(last, "s%d", &end); err != nil {
			return err
		}
		for i := start; i <= end; i++ {
			if got := w.stepCalls(fmt.Sprintf("s%d", i)); got != 1 {
				return fmt.Errorf("step s%d ran %d times, want 1", i, got)
			}
		}
		return nil
	})

	// Emitted events.
	sc.Step(`^a flow emits "([^"]*)", "([^"]*)", "([^"]*)", "([^"]*)", "([^"]*)" and "([^"]*)", calls (\d+) steps "([^"]*)" to "([^"]*)", checkpoints, calls (\d+) steps "([^"]*)" to "([^"]*)", then returns "([^"]*)"$`,
		func(ctx context.Context, a, b, c, d, e, f string, count int, first, last string, tail int, tailFirst, tailLast, literal string) error {
			return temporalW(ctx).addEmitFlow()
		})
	sc.Step(`^the run's durable events are "([^"]*)", "([^"]*)", "([^"]*)", "([^"]*)", "([^"]*)", "([^"]*)"$`, func(ctx context.Context, a, b, c, d, e, f string) error {
		w := temporalW(ctx)
		events, err := w.history(string(w.lastRef.TenantID), w.lastRef)
		if err != nil {
			return err
		}
		var got []string
		for _, event := range events {
			if event.Type == "event_emitted" {
				got = append(got, event.Name)
			}
		}
		want := []string{a, b, c, d, e, f}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			return fmt.Errorf("durable events %v, want %v", got, want)
		}
		return nil
	})

	// Parallel checkpoint.
	sc.Step(`^a flow calls (\d+) steps "([^"]*)" to "([^"]*)", starts steps "([^"]*)" and "([^"]*)" in parallel, calls checkpoint while "([^"]*)" is in flight, then returns "([^"]*)"$`,
		func(ctx context.Context, count int, first, last, one, two, inFlight, literal string) error {
			return temporalW(ctx).addParallelFlow()
		})
	sc.Step(`^steps "([^"]*)" to "([^"]*)", "([^"]*)" and "([^"]*)" were executed once each$`, func(ctx context.Context, first, last, one, two string) error {
		w := temporalW(ctx)
		var start, end int
		if _, err := fmt.Sscanf(first, "s%d", &start); err != nil {
			return err
		}
		if _, err := fmt.Sscanf(last, "s%d", &end); err != nil {
			return err
		}
		for i := start; i <= end; i++ {
			if got := w.stepCalls(fmt.Sprintf("s%d", i)); got != 1 {
				return fmt.Errorf("step s%d ran %d times, want 1", i, got)
			}
		}
		for _, name := range []string{one, two} {
			if got := w.stepCalls(name); got != 1 {
				return fmt.Errorf("step %q ran %d times, want 1", name, got)
			}
		}
		return nil
	})
	sc.Step(`^step "([^"]*)" completed before the continue-as-new$`, func(ctx context.Context, name string) error {
		w := temporalW(ctx)
		events, err := w.history(string(w.lastRef.TenantID), w.lastRef)
		if err != nil {
			return err
		}
		completed, continued := -1, -1
		for i, event := range events {
			if event.Type == "activity_completed" && event.Name == name {
				completed = i
			}
			if event.Type == "continued_as_new" {
				continued = i
			}
		}
		if completed < 0 || continued < 0 || completed > continued {
			return fmt.Errorf("step %q completed at %d, continue-as-new at %d", name, completed, continued)
		}
		return nil
	})

	// Version pinning.
	sc.Step(`^a simulated Temporal server version "([^"]*)"$`, func(ctx context.Context, version string) error {
		temporalW(ctx).version = version
		return nil
	})
	sc.Step(`^the temporal runtime starts against the Temporal test server$`, func(ctx context.Context) error {
		w := temporalW(ctx)
		if err := w.ensureRuntime(); err != nil {
			return err
		}
		serveCtx, cancel := context.WithCancel(context.Background())
		w.serveCancel = cancel
		go func() { _ = w.rt.Serve(serveCtx) }()
		select {
		case <-w.rt.Started():
			return nil
		case <-time.After(5 * time.Second):
			return fmt.Errorf("runtime did not start serving")
		}
	})
	sc.Step(`^the runtime is serving$`, func(ctx context.Context) error {
		if !temporalW(ctx).rt.Serving() {
			return fmt.Errorf("runtime is not serving")
		}
		return nil
	})
	sc.Step(`^the temporal runtime starts against that server$`, func(ctx context.Context) error {
		w := temporalW(ctx)
		if err := w.ensureRuntime(); err != nil {
			return err
		}
		w.lastErr = w.rt.Serve(context.Background())
		return nil
	})
	sc.Step(`^starting the runtime fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		err := temporalW(ctx).lastErr
		if err == nil {
			return fmt.Errorf("runtime started, want %s", code)
		}
		if got := string(core.CodeOf(err)); got != code {
			return fmt.Errorf("start code %q, want %q", got, code)
		}
		return nil
	})

	// Tenancy.
	sc.Step(`^tenant "([^"]*)" has run "([^"]*)" of flow "([^"]*)" on the temporal runtime waiting at gate "([^"]*)"$`, func(ctx context.Context, tenant, runID, flow, gate string) error {
		w := temporalW(ctx)
		ref, err := w.start(context.Background(), tenant, flow, runID, "")
		if err != nil {
			return err
		}
		w.lastRef = runtime.RunRef{RunID: runID, TenantID: core.TenantID(tenant)}
		if err := w.rt.WaitForGate(core.WithTenant(context.Background(), core.TenantID(tenant)), ref, gate); err != nil {
			return err
		}
		return nil
	})
	sc.Step(`^run "([^"]*)" is started on the temporal runtime with context tenant "([^"]*)" and request tenant "([^"]*)"$`, func(ctx context.Context, runID, contextTenant, requestTenant string) error {
		w := temporalW(ctx)
		_, err := w.start(context.Background(), contextTenant, "review", runID, optionalTenant(requestTenant))
		w.lastErr = err
		return nil
	})
	sc.Step(`^run "([^"]*)" of "([^"]*)" is started on the temporal runtime with context tenant "([^"]*)" and request tenant "([^"]*)"$`, func(ctx context.Context, runID, flow, contextTenant, requestTenant string) error {
		w := temporalW(ctx)
		_, err := w.start(context.Background(), contextTenant, flow, runID, optionalTenant(requestTenant))
		w.lastErr = err
		return nil
	})
	sc.Step(`^run "([^"]*)" of "([^"]*)" is started on the temporal runtime with context tenant "([^"]*)" and an empty request tenant$`, func(ctx context.Context, runID, flow, contextTenant string) error {
		w := temporalW(ctx)
		_, err := w.start(context.Background(), contextTenant, flow, runID, "")
		w.lastErr = err
		return nil
	})
	sc.Step(`^a run of "([^"]*)" is started on the temporal runtime with context tenant "([^"]*)" and request tenant "([^"]*)"$`, func(ctx context.Context, flow, contextTenant, requestTenant string) error {
		w := temporalW(ctx)
		_, err := w.start(context.Background(), contextTenant, flow, "r1", optionalTenant(requestTenant))
		w.lastErr = err
		return nil
	})
	sc.Step(`^a run of "([^"]*)" is started on the temporal runtime without a context tenant and with request tenant "([^"]*)"$`, func(ctx context.Context, flow, requestTenant string) error {
		w := temporalW(ctx)
		_, err := w.start(context.Background(), "", flow, "r1", optionalTenant(requestTenant))
		w.lastErr = err
		return nil
	})
	sc.Step(`^run "([^"]*)" of "([^"]*)" is started on the temporal runtime without a context tenant and with request tenant "([^"]*)"$`, func(ctx context.Context, runID, flow, requestTenant string) error {
		w := temporalW(ctx)
		_, err := w.start(context.Background(), "", flow, runID, optionalTenant(requestTenant))
		w.lastErr = err
		return nil
	})
	sc.Step(`^run "([^"]*)" is described on the temporal runtime with context tenant "([^"]*)"$`, func(ctx context.Context, runID, tenant string) error {
		w := temporalW(ctx)
		_, err := w.rt.Describe(core.WithTenant(context.Background(), core.TenantID(tenant)), w.lastRef)
		w.lastErr = err
		return nil
	})
	sc.Step(`^run "([^"]*)" is described on the temporal runtime with context tenant "([^"]*)" and an empty reference tenant$`, func(ctx context.Context, runID, tenant string) error {
		w := temporalW(ctx)
		_, err := w.rt.Describe(core.WithTenant(context.Background(), core.TenantID(tenant)), runtime.RunRef{RunID: runID})
		w.lastErr = err
		return nil
	})
	sc.Step(`^run "([^"]*)" is described on the temporal runtime without a context tenant and with reference tenant "([^"]*)"$`, func(ctx context.Context, runID, tenant string) error {
		w := temporalW(ctx)
		_, err := w.rt.Describe(context.Background(), runtime.RunRef{RunID: runID, TenantID: optionalTenant(tenant)})
		w.lastErr = err
		return nil
	})
	sc.Step(`^run "([^"]*)" is cancelled on the temporal runtime with context tenant "([^"]*)"$`, func(ctx context.Context, runID, tenant string) error {
		w := temporalW(ctx)
		w.lastErr = w.rt.Cancel(core.WithTenant(context.Background(), core.TenantID(tenant)), w.lastRef, "test")
		return nil
	})
	sc.Step(`^run "([^"]*)" is cancelled on the temporal runtime without a context tenant and with reference tenant "([^"]*)"$`, func(ctx context.Context, runID, tenant string) error {
		w := temporalW(ctx)
		w.lastErr = w.rt.Cancel(context.Background(), runtime.RunRef{RunID: runID, TenantID: optionalTenant(tenant)}, "test")
		return nil
	})
	sc.Step(`^tenant "([^"]*)" describes run "([^"]*)" as "([^"]*)"$`, func(ctx context.Context, tenant, runID, want string) error {
		if localActive(ctx) {
			lw := localStepWorld(ctx)
			state, err := lw.stateOf(ctx, tenant, runID)
			if err != nil {
				return err
			}
			if state != want {
				return fmt.Errorf("run %q of tenant %q is %q, want %q", runID, tenant, state, want)
			}
			return nil
		}
		w := temporalW(ctx)
		got, err := w.state(tenant, runtime.RunRef{RunID: runID})
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("tenant %q sees run %q as %q, want %q", tenant, runID, got, want)
		}
		return nil
	})
	sc.Step(`^gate "([^"]*)" of run "([^"]*)" is resolved on the temporal runtime with context tenant "([^"]*)" and payload (\{.*\})$`, func(ctx context.Context, gate, runID, tenant, payload string) error {
		w := temporalW(ctx)
		w.lastErr = w.rt.Resolve(core.WithTenant(context.Background(), core.TenantID(tenant)), w.lastRef, gate, json.RawMessage(payload))
		return nil
	})
	sc.Step(`^gate "([^"]*)" of run "([^"]*)" is resolved on the temporal runtime without a context tenant, reference tenant "([^"]*)" and payload (\{.*\})$`, func(ctx context.Context, gate, runID, tenant, payload string) error {
		w := temporalW(ctx)
		w.lastErr = w.rt.Resolve(context.Background(), runtime.RunRef{RunID: runID, TenantID: optionalTenant(tenant)}, gate, json.RawMessage(payload))
		return nil
	})
	sc.Step(`^runs of flow "([^"]*)" are listed on the temporal runtime for request tenant "([^"]*)" with context tenant "([^"]*)"$`, func(ctx context.Context, flow, requestTenant, contextTenant string) error {
		w := temporalW(ctx)
		_, _, w.lastErr = w.rt.List(core.WithTenant(context.Background(), core.TenantID(contextTenant)), runtime.RunQuery{Flow: flow, TenantID: optionalTenant(requestTenant)})
		return nil
	})
	sc.Step(`^runs of flow "([^"]*)" are listed on the temporal runtime with context tenant "([^"]*)" and an empty query tenant$`, func(ctx context.Context, flow, contextTenant string) error {
		w := temporalW(ctx)
		_, _, w.lastErr = w.rt.List(core.WithTenant(context.Background(), core.TenantID(contextTenant)), runtime.RunQuery{Flow: flow})
		return nil
	})
	sc.Step(`^runs of flow "([^"]*)" are listed on the temporal runtime without a context tenant and with query tenant "([^"]*)"$`, func(ctx context.Context, flow, requestTenant string) error {
		w := temporalW(ctx)
		_, _, w.lastErr = w.rt.List(context.Background(), runtime.RunQuery{Flow: flow, TenantID: optionalTenant(requestTenant)})
		return nil
	})
}

func temporalStepFailure(w *temporalWorld, code string) error {
	w.mu.Lock()
	err := w.lastStepErr
	w.mu.Unlock()
	if err == nil {
		return fmt.Errorf("step succeeded, want %s", code)
	}
	if got := string(core.CodeOf(err)); got != code {
		return fmt.Errorf("step code %q, want %q", got, code)
	}
	return nil
}

func temporalFailure(err error, code string) error {
	if err == nil {
		return fmt.Errorf("call succeeded, want %s", code)
	}
	if got := string(core.CodeOf(err)); got != code {
		return fmt.Errorf("code %q, want %q: %w", got, code, err)
	}
	return nil
}

// temporalValue parses the value a Then step names: a quoted string, a JSON
// object, or a size-and-seed payload.
func temporalValue(text string) json.RawMessage {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, `"`) {
		return json.RawMessage(text)
	}
	if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") || text == "null" || text == "true" || text == "false" {
		return json.RawMessage(text)
	}
	var size, seed int
	if _, err := fmt.Sscanf(text, "%d bytes of seed %d", &size, &seed); err == nil {
		return seedJSON(size, seed)
	}
	return json.RawMessage(text)
}
