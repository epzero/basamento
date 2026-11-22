package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"go.temporal.io/sdk/testsuite"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/blob"
	"github.com/epzero/basamento/platform/secrets"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/runtime/temporal"
)

// temporalControlState holds the last outcome of control-plane calls (Start,
// Describe, List, and similar) separate from data-path workflow execution.
type temporalControlState struct {
	lastErr            error
	lastStart          runtime.RunRef
	lastStatus         runtime.RunStatus
	lastList           []runtime.RunStatus
	pendingRef         runtime.RunRef
	haveListed         bool
	flowAttrRegistered bool
}

// temporalServeState owns the Serve goroutine and its cancellation.
type temporalServeState struct {
	cancel context.CancelFunc
	done   chan error
}

type temporalWorld struct {
	mu sync.Mutex

	suite  testsuite.WorkflowTestSuite
	env    *testsuite.TestWorkflowEnvironment
	rt     *temporal.Runtime
	blobs  *blob.Store
	blobAt string
	enc    *secrets.Codec
	sim    *simKeyRegistry

	version             string
	threshold           int
	checkpointThreshold int
	attempts            int

	flows       map[string]func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error)
	flowHarness map[string]flowHarnessMeta
	behaviors   map[string]*temporalStepBehavior
	registered  map[string]bool

	faultMode     string
	faultKey      string
	faultInjected bool

	history      []recordedPayload
	dataConv     *recordingConverter
	activeTenant core.TenantID

	lastErr     error
	lastStepErr error
	lastOutput  json.RawMessage

	outbox *memOutbox

	continuations    int
	execActivityDone atomic.Uint64
	currentExecIndex int
	orderProofStep   string
	stepBDoneExec    int
	continueFromExec int

	pendingGate pendingGateRun

	stub    *temporalStub
	control temporalControlState
	serve   temporalServeState
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

func copyMeta(in map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

func newTemporalWorld() *temporalWorld {
	return &temporalWorld{
		version:          "1.32.2",
		flows:            map[string]func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error){},
		flowHarness:      map[string]flowHarnessMeta{},
		behaviors:        map[string]*temporalStepBehavior{},
		registered:       map[string]bool{},
		stepBDoneExec:    -1,
		continueFromExec: -1,
	}
}

func temporalW(ctx context.Context) *temporalWorld {
	w := world(ctx)
	if w.Temporal == nil {
		w.Temporal = newTemporalWorld()
	}
	return w.Temporal
}

func (w *temporalWorld) close() error {
	serveErr := w.shutdownServe()
	if w.stub != nil {
		w.stub.Close()
		w.stub = nil
	}
	if w.blobs != nil {
		_ = w.blobs.Close()
	}
	if w.blobAt != "" {
		_ = os.RemoveAll(w.blobAt)
	}
	return serveErr
}

func (w *temporalWorld) shutdownServe() error {
	w.mu.Lock()
	cancel := w.serve.cancel
	done := w.serve.done
	w.serve.cancel = nil
	w.serve.done = nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	serveErr := <-done
	if serveErr != nil {
		return fmt.Errorf("temporal: Serve returned an error after it was cancelled: %w", serveErr)
	}
	return nil
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
	if w.rt != nil {
		return nil
	}
	if w.blobs == nil {
		return fmt.Errorf("blob store is not prepared")
	}
	if w.stub == nil {
		return fmt.Errorf("temporal test server is not ready")
	}
	ob := w.bddOutbox()
	clientOpts := w.stub.ClientOptions()
	rt, err := temporal.New(temporal.Options{
		Payloads:                 w.enc,
		Blobs:                    w.blobs,
		ClaimCheckBytes:          w.threshold,
		Outbox:                   ob,
		CheckpointEventThreshold: w.ckptThreshold(),
		Client:                   clientOpts,
	})
	if err != nil {
		return err
	}
	if err := rt.RegisterFlowHost(w.runFlow); err != nil {
		return err
	}
	w.rt = rt
	return nil
}

func (w *temporalWorld) ckptThreshold() int {
	if w.checkpointThreshold > 0 {
		return w.checkpointThreshold
	}
	return temporal.DefaultCheckpointEventThreshold
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

func (w *temporalWorld) ensureSteps(names ...string) error {
	for _, name := range names {
		if err := w.ensureStep(name); err != nil {
			return err
		}
	}
	return nil
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
	proof := w.orderProofStep
	exec := w.currentExecIndex
	w.mu.Unlock()
	if name == proof {
		w.mu.Lock()
		w.stepBDoneExec = exec
		w.mu.Unlock()
	}
	w.execActivityDone.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, core.NewError(core.CodeCancelled, "temporal bdd: step cancelled", nil)
	}
	if shouldFail && code != "" {
		return nil, &core.Error{Code: code, Message: "temporal bdd: step failed", Retryable: retryable}
	}
	return append(json.RawMessage(nil), output...), nil
}

func (w *temporalWorld) start(tenant, flowName, runID string) error {
	return w.runChain(chainRunConfig{
		tenant: core.TenantID(tenant),
		flow:   flowName,
		runID:  runID,
	})
}

func (w *temporalWorld) waitDone() {
	if w.env == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastErr = w.env.GetWorkflowError()
	if w.lastErr == nil && w.env.IsWorkflowCompleted() {
		if err := w.env.GetWorkflowResult(&w.lastOutput); err != nil {
			w.lastErr = err
		}
	}
}

func (w *temporalWorld) runState() string {
	if w.env == nil {
		return "running"
	}
	if err := w.env.GetWorkflowError(); err != nil {
		return "failed"
	}
	if w.env.IsWorkflowCompleted() {
		return "completed"
	}
	return "running"
}

func (w *temporalWorld) recorded() []recordedPayload {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]recordedPayload, len(w.history))
	copy(out, w.history)
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
