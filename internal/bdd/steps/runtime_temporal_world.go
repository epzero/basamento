package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/blob"
	"github.com/epzero/basamento/platform/secrets"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/runtime/temporal"
)

type temporalWorld struct {
	mu sync.Mutex

	suite  testsuite.WorkflowTestSuite
	env    *testsuite.TestWorkflowEnvironment
	rt     *temporal.Runtime
	blobs  *blob.Store
	blobAt string
	enc    *secrets.Codec
	codec  *temporal.Codec
	sim    *simKeyRegistry

	version   string
	threshold int
	attempts  int

	flows      map[string]func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error)
	behaviors  map[string]*temporalStepBehavior
	registered map[string]bool

	faultMode  string
	faultKey   string
	faultAfter int

	recorder     *historyRecorder
	dataConv     *temporal.DataConverter
	activeTenant core.TenantID

	lastErr     error
	lastStepErr error
	lastOutput  json.RawMessage
}

type temporalStepBehavior struct {
	code      core.Code
	retryable bool
	failFirst int
	calls     int
	output    json.RawMessage
}

type recordedPayload struct {
	metadata   map[string][]byte
	data       []byte
	claimCheck bool
}

type historyRecorder struct {
	world *temporalWorld
	enc   *secrets.Codec
	mu    sync.Mutex
	seen  []recordedPayload
	count int
}

func (h *historyRecorder) maybeFault(enc temporal.Encoded, current *commonpb.Payload) *commonpb.Payload {
	h.mu.Lock()
	h.count++
	n := h.count
	h.mu.Unlock()
	h.world.mu.Lock()
	mode, key, after := h.world.faultMode, h.world.faultKey, h.world.faultAfter
	h.world.mu.Unlock()
	if mode == "" || n <= after {
		return nil
	}
	switch mode {
	case "plain":
		return &commonpb.Payload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte(`{"ok":true}`)}
	case "foreign":
		marker, _ := json.Marshal(struct {
			Key  string `json:"key"`
			Size int    `json:"size"`
		}{Key: key, Size: 8192})
		encMarker, err := h.enc.Encode(core.WithTenant(context.Background(), "acme"), secrets.Payload{
			Metadata: map[string][]byte{"encoding": []byte(temporal.ClaimCheckEncoding)},
			Data:     marker,
		})
		if err != nil {
			return nil
		}
		return &commonpb.Payload{Metadata: encMarker.Metadata, Data: encMarker.Data}
	}
	return nil
}

func (h *historyRecorder) observe(encoded temporal.Encoded) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seen = append(h.seen, recordedPayload{
		metadata:   copyMeta(encoded.Payload.Metadata),
		data:       append([]byte(nil), encoded.Payload.Data...),
		claimCheck: encoded.ClaimCheck,
	})
}

func copyMeta(in map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

func newTemporalWorld() *temporalWorld {
	return &temporalWorld{
		version:    "1.32.2",
		flows:      map[string]func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error){},
		behaviors:  map[string]*temporalStepBehavior{},
		registered: map[string]bool{},
	}
}

func temporalW(ctx context.Context) *temporalWorld {
	w := world(ctx)
	if w.Temporal == nil {
		w.Temporal = newTemporalWorld()
	}
	return w.Temporal
}

func (w *temporalWorld) close() {
	if w.env != nil {
		w.env.AssertExpectations(nil)
	}
	if w.blobs != nil {
		_ = w.blobs.Close()
	}
	if w.blobAt != "" {
		_ = os.RemoveAll(w.blobAt)
	}
}

func (w *temporalWorld) prepare() error {
	w.mu.Lock()
	defer w.mu.Unlock()
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

func (w *temporalWorld) ensureRuntime() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.env != nil {
		return nil
	}
	if w.blobs == nil {
		return fmt.Errorf("blob store is not prepared")
	}
	codec, err := temporal.NewCodec(w.enc, w.blobs, w.threshold)
	if err != nil {
		return err
	}
	w.codec = codec
	w.recorder = &historyRecorder{world: w, enc: w.enc}
	rt, err := temporal.New(temporal.Options{Codec: codec, Blobs: w.blobs, Threshold: w.threshold})
	if err != nil {
		return err
	}
	if err := rt.RegisterFlowHost(w.runFlow); err != nil {
		return err
	}
	w.rt = rt
	w.env = w.suite.NewTestWorkflowEnvironment()
	w.env.SetTestTimeout(time.Hour)
	w.env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: time.Hour})
	w.env.SetContextPropagators([]workflow.ContextPropagator{temporal.TenantPropagator{}})
	rawDC, err := rt.BuildDataConverter(w.recorder.observe)
	if err != nil {
		return err
	}
	w.dataConv = rawDC.(*temporal.DataConverter)
	w.dataConv.FaultReplace = w.recorder.maybeFault
	w.env.SetDataConverter(w.dataConv)
	if err := rt.RegisterWorkflowsActivities(w.env); err != nil {
		return err
	}
	for name := range w.behaviors {
		if err := w.registerStepLocked(name); err != nil {
			return err
		}
	}
	return nil
}

func (w *temporalWorld) setDataConverterTenant(tenant core.TenantID) {
	if w.dataConv == nil {
		return
	}
	w.env.SetDataConverter(w.dataConv.WithFallbackTenant(tenant))
}

func (w *temporalWorld) setTenantHeader(tenant core.TenantID) {
	p, _ := converter.GetDefaultDataConverter().ToPayload(string(tenant))
	w.env.SetHeader(&commonpb.Header{Fields: map[string]*commonpb.Payload{"basamento-tenant": p}})
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

func (w *temporalWorld) start(tenant, flowName, runID string) error {
	if err := w.ensureRuntime(); err != nil {
		return err
	}
	if err := w.ensureNamedFlow(flowName); err != nil {
		return err
	}
	id := core.TenantID(tenant)
	w.activeTenant = id
	w.setDataConverterTenant(id)
	w.setTenantHeader(id)
	w.env.ExecuteWorkflow(temporal.HostWorkflowName, temporal.HostInput{
		Flow:   runtime.FlowRef{Name: flowName},
		RunID:  runID,
		Tenant: id,
	})
	w.mu.Lock()
	w.lastErr = w.env.GetWorkflowError()
	if w.lastErr == nil {
		_ = w.env.GetWorkflowResult(&w.lastOutput)
	}
	w.mu.Unlock()
	return nil
}

func (w *temporalWorld) waitDone() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastErr = w.env.GetWorkflowError()
	if w.lastErr == nil {
		_ = w.env.GetWorkflowResult(&w.lastOutput)
	}
}

func (w *temporalWorld) runState() string {
	if err := w.env.GetWorkflowError(); err != nil {
		return "failed"
	}
	return "completed"
}

func (w *temporalWorld) recorded() []recordedPayload {
	w.recorder.mu.Lock()
	defer w.recorder.mu.Unlock()
	out := make([]recordedPayload, len(w.recorder.seen))
	copy(out, w.recorder.seen)
	return out
}

func (w *temporalWorld) stepCalls(name string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if b := w.behaviors[name]; b != nil {
		return b.calls
	}
	return 0
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
