package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/schema"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/catalog"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/runtime"
)

// AmbiguousStartGate is the gate a run waits at when a start's outcome is
// unknown and the provider cannot resolve it by lookup (REQ-MODEL-005). A
// person resolves it with a payload naming the invocation, if there is one.
const AmbiguousStartGate = "ambiguous_start"

// visitToken is the id kind the node draws once per visit of one node in one
// run. It is run-scoped and deterministic, which is what makes the client ref
// stable across a replay (see ClientRef).
const visitToken = "model-node"

const stepHeartbeat = 30 * time.Second

// maxRegisteredSteps bounds the step names the handler remembers. Registration
// tolerates a conflict, so sweeping the memo only means the next use registers
// again; this keeps a process that serves many runs from growing without
// bound. It is a cache, not a registry: the runtime owns the registered steps.
const maxRegisteredSteps = 4096

// Config is a model node's spec: which model it invokes and how its call is
// sized for the price estimate. The spec is the node's IR `spec` object:
//
//	{"model": "sim-video", "op": "video.generate", "output_limit": 0}
type Config struct {
	// Name is the node's id in the flow. The host fills it in; it is part of
	// the client ref and of every step name.
	Name string
	// Model is the model id the catalog holds.
	Model string `json:"model"`
	// Op is the capability the call asks for.
	Op model.Capability `json:"op"`
	// InputTokens is the counted input tokens of the call, used by the price
	// estimate (REQ-MODEL-077). Zero when the model's formula does not price
	// tokens.
	InputTokens int64 `json:"input_tokens,omitempty"`
	// OutputLimit is the call's output token limit, used by the estimate.
	OutputLimit int64 `json:"output_limit,omitempty"`
}

// ConfigFromSpec decodes a model node's IR spec. name is the node's key in the
// flow, which the host passes separately because it is not part of the IR node
// object.
func ConfigFromSpec(name string, spec json.RawMessage) (Config, error) {
	if name == "" {
		return Config{}, core.NewError(core.CodeInvalidArgument, "model node has no name", nil)
	}
	cfg := Config{Name: name}
	if len(bytes.TrimSpace(spec)) > 0 {
		if err := json.Unmarshal(spec, &cfg); err != nil {
			return Config{}, core.NewError(core.CodeInvalidArgument,
				fmt.Sprintf("model node %q: invalid spec", name), map[string]any{"node": name, "cause": err.Error()})
		}
	}
	cfg.Name = name
	if cfg.Model == "" {
		return Config{}, core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("model node %q: spec has no model", name), map[string]any{"node": name})
	}
	if cfg.Op == "" {
		return Config{}, core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("model node %q: spec has no op", name), map[string]any{"node": name})
	}
	return cfg, nil
}

// Input is a model node's input: the call's parameters and media, as the
// flow's inbound mapping produces them.
type Input struct {
	Params       json.RawMessage             `json:"params,omitempty"`
	Messages     json.RawMessage             `json:"messages,omitempty"`
	Tools        json.RawMessage             `json:"tools,omitempty"`
	OutputSchema json.RawMessage             `json:"output_schema,omitempty"`
	Text         map[string]string           `json:"text,omitempty"`
	Assets       map[string][]model.AssetRef `json:"assets,omitempty"`
	Seed         *int64                      `json:"seed,omitempty"`
	InputTokens  int64                       `json:"input_tokens,omitempty"`
	OutputLimit  int64                       `json:"output_limit,omitempty"`
}

// Output is what the node returns to the flow: the invocation's result and the
// cost that settled it.
type Output struct {
	State        model.State     `json:"state"`
	ClientRef    string          `json:"client_ref"`
	Message      json.RawMessage `json:"message,omitempty"`
	Vectors      [][]float32     `json:"vectors,omitempty"`
	Assets       []Asset         `json:"assets,omitempty"`
	Usage        *model.Usage    `json:"usage,omitempty"`
	CostMpts     int64           `json:"cost_mpts"`
	ReservedMpts int64           `json:"reserved_mpts"`
}

// StepRegistrar registers the handler's durable steps. runtime.Runtime
// satisfies it. The handler registers a step the first time a run needs it,
// because a step's name carries the invocation's client ref: two visits of one
// node in a run must not share one recorded result. A runtime with a static
// registry (the Temporal runtime) registers the same names from the flow
// definitions before its workers start.
type StepRegistrar interface {
	RegisterStep(name string, fn runtime.StepFunc, d runtime.StepDefaults) error
}

// Deps is what a model node handler needs. Invoker and Catalog are required;
// the rest is optional.
type Deps struct {
	// Invoker is the provider adapter the node starts and follows.
	Invoker model.Invoker
	// Catalog holds descriptors and prices. The node validates against the
	// descriptor's parameter schema and prices the call with it
	// (REQ-MODEL-067, REQ-MODEL-077).
	Catalog *catalog.Catalog
	// Steps registers the node's durable steps on first use.
	Steps StepRegistrar
	// Blobs stores output files ingested with provenance (REQ-MODEL-008).
	// It is required for a model that returns output files: a successful
	// invocation with outputs and no blob store fails the node instead of
	// dropping them.
	Blobs platform.Blob
	// Fetch downloads an output. Nil uses HTTP for a URL and the filesystem
	// for a path.
	Fetch Fetcher
	// MaxStartAttempts bounds the start attempts of one invocation; zero
	// means DefaultStartAttempts.
	MaxStartAttempts int
}

// Handler is the model node handler. Create one per composition and share it
// across runs; the run's budget travels in each run's context (WithBudget).
type Handler struct {
	deps  Deps
	start Starter

	mu         sync.Mutex
	registered map[string]bool
}

// New returns a handler over deps.
func New(d Deps) (*Handler, error) {
	if d.Invoker == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "model node needs an invoker", nil)
	}
	if d.Catalog == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "model node needs a catalog", nil)
	}
	return &Handler{
		deps:       d,
		start:      Starter{Invoker: d.Invoker, MaxAttempts: d.MaxStartAttempts},
		registered: map[string]bool{},
	}, nil
}

// Handle runs one model node. It validates, prices, reserves, starts, follows
// the invocation, waits for its result, ingests outputs and settles the
// reservation, in that order (docs/architecture/model-layer.md §4).
//
// The returned error is a platform error: invalid_argument, budget_exceeded,
// unsupported, vendor_error, cancelled, ambiguous_start or the provider's own
// code. The reservation is settled on every path after it was taken. When the
// invocation started but its actual charge is unknown, the node fails closed
// and charges the reserved estimate rather than releasing it.
func (h *Handler) Handle(dc runtime.DurableContext, cfg Config, input json.RawMessage) (out Output, retErr error) {
	if dc == nil {
		return Output{}, core.NewError(core.CodeInternal, "model node: nil durable context", nil)
	}
	if cfg.Name == "" || cfg.Model == "" {
		return Output{}, core.NewError(core.CodeInvalidArgument, "model node: spec has no name or model", map[string]any{"node": cfg.Name})
	}
	in, err := decodeInput(input)
	if err != nil {
		return Output{}, err
	}
	desc, err := h.deps.Catalog.Lookup(dc.Context(), cfg.Model)
	if err != nil {
		return Output{}, err
	}
	if err := checkCapability(desc, cfg.Op); err != nil {
		return Output{}, err
	}
	if err := validateParams(desc, in.Params); err != nil {
		return Output{}, err
	}

	ref := ClientRef(runSalt(dc), dc.NewID(visitToken), cfg.Name, refInput(input))
	req := request(cfg, in, ref)

	// Every priced invocation reserves before it starts (REQ-MODEL-006): a
	// priced call with no run budget, or one the price table cannot price,
	// fails before the provider is reached. A genuinely unpriced model may
	// still run unmetered; with a budget, an unpriced model fails with
	// unsupported (REQ-MODEL-073). Pricing is a durable step: its recorded pin
	// is what every execution, first and replay, reserves and settles by, so a
	// changed price layer cannot reprice a started call (REQ-MODEL-078).
	budget, metered := BudgetFrom(dc.Context())
	pricedOut, err := h.priceInvocation(dc, cfg, in, ref, metered)
	if err != nil {
		return Output{}, err
	}
	if pricedOut.Quote == nil && !pricedOut.Unpriced {
		return Output{}, core.NewError(core.CodeInternal,
			fmt.Sprintf("model node %q: the price step returned no quote", cfg.Name),
			map[string]any{"node": cfg.Name, "model": cfg.Model})
	}
	var quote catalog.Quote
	priced := pricedOut.Quote != nil
	if priced {
		quote = *pricedOut.Quote
	}
	if priced && !metered {
		return Output{}, core.NewError(core.CodeInternal,
			fmt.Sprintf("model node %q: a priced invocation needs a run budget", cfg.Name),
			map[string]any{"node": cfg.Name, "model": cfg.Model})
	}
	// A priced call that estimates at zero would reserve nothing and let its
	// actual usage charge past the limit; fail closed instead of trusting an
	// undeclared usage.
	if priced && quote.Estimate <= 0 {
		return Output{}, core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("model node %q: a priced call to %q has no positive estimate; declare the call's usage", cfg.Name, cfg.Model),
			map[string]any{"node": cfg.Name, "model": cfg.Model})
	}

	out = Output{ClientRef: ref}
	var charged int64
	settled := false
	if priced {
		if err := budget.Reserve(ref, quote.Estimate); err != nil {
			return Output{}, err
		}
		out.ReservedMpts = quote.Estimate
	}
	// Every path after the reservation settles it (REQ-MODEL-006). A failure
	// before the invocation started releases it; a failure after it started
	// leaves the actual charge unknown, and the node fails closed by charging
	// the reserved estimate. Settlement is first-wins, so releasing there
	// would make the undercharge permanent.
	started := false
	defer func() {
		if !priced || settled {
			return
		}
		actual := charged
		if started {
			actual = quote.Estimate
		}
		// Do not mark the reservation settled unless the budget accepted it:
		// a failed settlement leaves it open for the next attempt.
		if err := budget.Settle(ref, actual); err != nil {
			if retErr == nil {
				retErr = err
			}
			return
		}
		settled = true
		charged = actual
	}()
	settle := func(actual int64) error {
		if priced && !settled {
			if err := budget.Settle(ref, actual); err != nil {
				return err
			}
			settled = true
			charged = actual
		}
		out.CostMpts = actual
		return nil
	}

	startOutcome, err := h.startInvocation(dc, cfg, req, ref)
	if err != nil {
		return Output{}, err
	}
	started = true
	if startOutcome.Ambiguous {
		handle, err := h.resolveAmbiguous(dc, cfg, ref)
		if err != nil {
			return Output{}, err
		}
		startOutcome.Handle = handle
	}

	followed, err := h.followInvocation(dc, cfg, startOutcome.Handle, ref)
	if err != nil {
		return Output{}, err
	}

	// Settle before anything that can fail after the invocation ended: the
	// provider already ran and may have charged.
	result := followed.Result
	actual, err := h.actualCost(result, quote, priced, followed.Cancelled)
	if err != nil {
		return Output{}, err
	}
	if err := settle(actual); err != nil {
		return out, err
	}

	out.State = result.State
	out.Message = result.Message
	out.Vectors = result.Vectors
	out.Usage = result.Usage

	// A cancellation or a failed invocation never ingests: checking it first
	// keeps an ingest failure from masking the real outcome (REQ-MODEL-009).
	if followed.Cancelled {
		return out, core.NewError(core.CodeCancelled, "model node: invocation cancelled with the run", nil)
	}
	if result.State == model.StateFailed {
		return out, failureError(result)
	}
	if len(result.Outputs) > 0 {
		// REQ-MODEL-008 requires ingestion; silently dropping a successful
		// invocation's outputs would lose them, so fail closed instead.
		if h.deps.Blobs == nil {
			return out, core.NewError(core.CodeInternal,
				"model node: the invocation returned output files but no blob store is configured",
				map[string]any{"node": cfg.Name, "outputs": len(result.Outputs)})
		}
		assets, err := h.ingestOutputs(dc, cfg, req, result, actual, ref)
		if err != nil {
			return out, err
		}
		out.Assets = assets
	}
	return out, nil
}

// priceInvocation runs the price step. Its result is the durable pin the
// rest of the visit uses: the estimate, the price entry and the instant the
// call was priced at.
func (h *Handler) priceInvocation(dc runtime.DurableContext, cfg Config, in Input, ref string, metered bool) (priceOutcome, error) {
	name := stepName(cfg, "price", ref)
	nonRetryable := []string{string(core.CodeUnsupported), string(core.CodeInvalidArgument)}
	if err := h.register(name, h.priceStep, stepDefaults(nonRetryable...)); err != nil {
		return priceOutcome{}, err
	}
	opts := stepDefaults(nonRetryable...).Options
	opts.IdempotencyKey = name
	var out priceOutcome
	if err := dc.Step(name, priceIn{
		Model:       cfg.Model,
		Params:      in.Params,
		InputTokens: inputTokens(cfg, in),
		OutputLimit: outputLimit(cfg, in),
		// Price at the run's clock, not the catalog's process clock, so a
		// replay prices a time-based rule at the same instant (REQ-MODEL-078).
		At:      dc.Now(),
		Metered: metered,
	}, opts).Get(&out); err != nil {
		return priceOutcome{}, err
	}
	return out, nil
}

// priceStep is the durable step behind priceInvocation. It records an
// unpriced model as a successful outcome when the run has no budget; with a
// budget the catalog's unsupported error stops the step without retrying.
func (h *Handler) priceStep(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var in priceIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "model node: invalid price step input", nil)
	}
	quote, err := h.deps.Catalog.Start(ctx, catalog.Request{
		Model:       in.Model,
		Params:      in.Params,
		InputTokens: in.InputTokens,
		OutputLimit: in.OutputLimit,
		At:          in.At,
	})
	if err != nil {
		if core.CodeOf(err) == core.CodeUnsupported && !in.Metered {
			return json.Marshal(priceOutcome{Unpriced: true})
		}
		return nil, err
	}
	return json.Marshal(priceOutcome{Quote: &quote})
}

// startInvocation runs the start step.
func (h *Handler) startInvocation(dc runtime.DurableContext, cfg Config, req model.Request, ref string) (StartOutcome, error) {
	name := stepName(cfg, "start", ref)
	if err := h.register(name, h.startStep, stepDefaults()); err != nil {
		return StartOutcome{}, err
	}
	var out StartOutcome
	if err := dc.Step(name, startIn{Request: req}, runtime.StepOptions{
		Heartbeat:      stepHeartbeat,
		IdempotencyKey: name,
	}).Get(&out); err != nil {
		return StartOutcome{}, err
	}
	return out, nil
}

// startStep is the durable step behind startInvocation.
func (h *Handler) startStep(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var in startIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "model node: invalid start step input", nil)
	}
	out, err := h.start.Start(ctx, in.Request)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// resolveAmbiguous opens the ambiguous-start gate and looks the invocation up
// again once it is resolved. The gate's payload may carry the handle as
// {"handle": {...}}; otherwise the node looks it up by client ref.
func (h *Handler) resolveAmbiguous(dc runtime.DurableContext, cfg Config, ref string) (model.Handle, error) {
	var payload struct {
		Handle *model.Handle `json:"handle,omitempty"`
	}
	if err := dc.Await(runtime.GateSpec{
		Name:   AmbiguousStartGate,
		Schema: json.RawMessage(`{"type":"object"}`),
	}).Get(&payload); err != nil {
		return model.Handle{}, err
	}
	if payload.Handle != nil {
		// One gate serves the whole run, so a handle from another node's
		// invocation may be waiting there; only this invocation's ref is
		// acceptable.
		if payload.Handle.ClientRef != ref {
			return model.Handle{}, core.NewError(core.CodeAmbiguousStart,
				fmt.Sprintf("model node %q: the gate's handle belongs to another invocation", cfg.Name),
				map[string]any{"node": cfg.Name, "client_ref": ref, "cause": "handle_ref_mismatch"})
		}
		return *payload.Handle, nil
	}
	name := stepName(cfg, "lookup", ref)
	if err := h.register(name, h.lookupStep, stepDefaults()); err != nil {
		return model.Handle{}, err
	}
	var out lookupOutcome
	if err := dc.Step(name, lookupIn{ClientRef: ref}, runtime.StepOptions{IdempotencyKey: name}).Get(&out); err != nil {
		return model.Handle{}, core.NewError(core.CodeAmbiguousStart,
			fmt.Sprintf("model node %q: the start of %q cannot be resolved", cfg.Name, ref),
			map[string]any{"node": cfg.Name, "client_ref": ref, "cause": "lookup_failed"})
	}
	if !out.Found {
		return model.Handle{}, core.NewError(core.CodeAmbiguousStart,
			fmt.Sprintf("model node %q: the start of %q is still unknown after the gate", cfg.Name, ref),
			map[string]any{"node": cfg.Name, "client_ref": ref})
	}
	return out.Handle, nil
}

func (h *Handler) lookupStep(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var in lookupIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "model node: invalid lookup step input", nil)
	}
	handle, err := h.deps.Invoker.Lookup(ctx, in.ClientRef)
	if err != nil {
		return nil, model.MapError(err)
	}
	if handle == nil {
		return json.Marshal(lookupOutcome{})
	}
	return json.Marshal(lookupOutcome{Found: true, Handle: *handle})
}

// followInvocation follows the invocation's events and waits for its end. A
// run cancellation cancels the invocation at the provider and settles by the
// result it reports (REQ-MODEL-009).
func (h *Handler) followInvocation(dc runtime.DurableContext, cfg Config, handle model.Handle, ref string) (followOutcome, error) {
	name := stepName(cfg, "follow", ref)
	if err := h.register(name, h.followStep, stepDefaults()); err != nil {
		return followOutcome{}, err
	}
	var out followOutcome
	if err := dc.Step(name, followIn{Handle: handle, ClientRef: ref}, runtime.StepOptions{
		Heartbeat:      stepHeartbeat,
		IdempotencyKey: name,
	}).Get(&out); err != nil {
		return followOutcome{}, err
	}
	return out, nil
}

func (h *Handler) followStep(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var in followIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "model node: invalid follow step input", nil)
	}
	events, err := h.deps.Invoker.Events(ctx, in.Handle, 0)
	if err != nil {
		return nil, model.MapError(err)
	}
	// Drain the provider's stream: progress deltas are bus-only
	// (docs/architecture/streaming.md §2, §4) and publishing them is
	// WP-L6-02's job. The durable step keeps the final result, not the log.
	for range events {
	}
	if ctx.Err() != nil {
		// The run was cancelled while the invocation was in flight. Cancel at
		// the provider with a context the cancellation no longer reaches, then
		// take the final state the provider reports.
		detached := context.WithoutCancel(ctx)
		if err := h.deps.Invoker.Cancel(detached, in.Handle); err != nil && !errors.Is(err, context.Canceled) {
			return nil, model.MapError(err)
		}
		result, err := h.deps.Invoker.Result(detached, in.Handle)
		if err != nil {
			return nil, model.MapError(err)
		}
		return json.Marshal(followOutcome{Result: result, Cancelled: true})
	}
	result, err := h.deps.Invoker.Result(ctx, in.Handle)
	if err != nil {
		return nil, model.MapError(err)
	}
	return json.Marshal(followOutcome{Result: result})
}

// actualCost is what the run is charged: the cost the provider reports, or
// else the price formula on the actual usage (docs/architecture/model-layer.md
// §7).
//
// A started, priced invocation that completed without reporting a cost or a
// usage is charged the reserved estimate, not zero: an absent usage is not
// proof the provider charged nothing (poll and video results often carry none).
// The one exception is a cancellation whose provider reports no cost: the run
// stops the invocation, so it releases the reservation (REQ-MODEL-009).
func (h *Handler) actualCost(result model.Result, quote catalog.Quote, priced, cancelled bool) (int64, error) {
	if result.CostMpts > 0 {
		return result.CostMpts, nil
	}
	if result.Usage == nil {
		if priced && !cancelled {
			return quote.Estimate, nil
		}
		return 0, nil
	}
	if !priced {
		return 0, nil
	}
	charge, err := h.deps.Catalog.Settle(quote, *result.Usage)
	if err != nil {
		return 0, err
	}
	return charge.MilliPoints, nil
}

// stepDefaults are the options shared by the handler's steps. A step's
// registration and its call use the same defaults.
func stepDefaults(nonRetryable ...string) runtime.StepDefaults {
	return runtime.StepDefaults{Options: runtime.StepOptions{
		Heartbeat:    stepHeartbeat,
		NonRetryable: nonRetryable,
	}}
}

// register installs one of the handler's steps with the runtime, once per
// name until the memo is swept. A conflict is success: another run of the same
// flow registered the same name, and that step's function is the same.
func (h *Handler) register(name string, fn runtime.StepFunc, defaults runtime.StepDefaults) error {
	if h.deps.Steps == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.registered[name] {
		return nil
	}
	if len(h.registered) >= maxRegisteredSteps {
		h.registered = map[string]bool{}
	}
	err := h.deps.Steps.RegisterStep(name, fn, defaults)
	if err != nil && core.CodeOf(err) != core.CodeConflict {
		return err
	}
	h.registered[name] = true
	return nil
}

func stepName(cfg Config, phase, ref string) string {
	return "model." + cfg.Name + "." + phase + "." + ref
}

// decodeInput parses a node input. An empty input is an empty call.
func decodeInput(raw json.RawMessage) (Input, error) {
	var in Input
	if len(bytes.TrimSpace(raw)) == 0 {
		return in, nil
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return Input{}, core.NewError(core.CodeInvalidArgument, "model node: invalid input", map[string]any{"cause": err.Error()})
	}
	return in, nil
}

// refInput is the bytes the client ref is derived from: the node input, or an
// empty object when the node has none.
func refInput(raw json.RawMessage) []byte {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte("{}")
	}
	return raw
}

func request(cfg Config, in Input, ref string) model.Request {
	return model.Request{
		Model:        cfg.Model,
		Op:           cfg.Op,
		Messages:     in.Messages,
		Tools:        in.Tools,
		OutputSchema: in.OutputSchema,
		Text:         in.Text,
		Assets:       in.Assets,
		Params:       in.Params,
		Seed:         in.Seed,
		ClientRef:    ref,
	}
}

func inputTokens(cfg Config, in Input) int64 {
	if in.InputTokens > 0 {
		return in.InputTokens
	}
	return cfg.InputTokens
}

func outputLimit(cfg Config, in Input) int64 {
	if in.OutputLimit > 0 {
		return in.OutputLimit
	}
	return cfg.OutputLimit
}

// checkCapability rejects a call the model does not declare.
func checkCapability(desc model.Descriptor, op model.Capability) error {
	for _, c := range desc.Caps {
		if c == op {
			return nil
		}
	}
	return core.NewError(core.CodeUnsupported,
		fmt.Sprintf("model %q does not support %s", desc.ID, op),
		map[string]any{"model": desc.ID, "op": string(op)})
}

// validateParams validates the call's parameters against the model's parameter
// schema before anything is priced or started (REQ-MODEL-010). The schema's
// failures are invalid_argument with the schema package's locations.
func validateParams(desc model.Descriptor, params json.RawMessage) error {
	if len(bytes.TrimSpace(desc.Params)) == 0 {
		return nil
	}
	compiled, err := schema.Compile(desc.Params)
	if err != nil {
		return core.NewError(core.CodeInternal,
			fmt.Sprintf("model %q: parameter schema is invalid", desc.ID),
			map[string]any{"model": desc.ID})
	}
	if len(bytes.TrimSpace(params)) == 0 {
		params = json.RawMessage(`{}`)
	}
	if err := compiled.Validate(params); err != nil {
		if core.CodeOf(err) == core.CodeInvalidArgument {
			return err
		}
		return core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("model %q: parameters do not satisfy the parameter schema", desc.ID),
			map[string]any{"model": desc.ID})
	}
	return nil
}

func failureError(result model.Result) error {
	var failure model.Failure
	if len(result.Error) > 0 && json.Unmarshal(result.Error, &failure) == nil && failure.Code != "" {
		return &core.Error{Code: failure.Code, Message: failure.Message, Retryable: failure.Retryable}
	}
	return core.NewError(core.CodeVendor, "model node: the invocation failed", nil)
}

// startIn is the start step's payload.
type startIn struct {
	Request model.Request `json:"request"`
}

// lookupIn and lookupOutcome are the lookup step's payloads.
type lookupIn struct {
	ClientRef string `json:"client_ref"`
}

type lookupOutcome struct {
	Found  bool         `json:"found"`
	Handle model.Handle `json:"handle"`
}

// followIn and followOutcome are the follow step's payloads.
type followIn struct {
	Handle    model.Handle `json:"handle"`
	ClientRef string       `json:"client_ref"`
}

type followOutcome struct {
	Result    model.Result `json:"result"`
	Cancelled bool         `json:"cancelled,omitempty"`
}

// priceIn is the price step's payload.
type priceIn struct {
	Model       string          `json:"model"`
	Params      json.RawMessage `json:"params,omitempty"`
	InputTokens int64           `json:"input_tokens,omitempty"`
	OutputLimit int64           `json:"output_limit,omitempty"`
	At          time.Time       `json:"at"`
	Metered     bool            `json:"metered,omitempty"`
}

// priceOutcome is the price step's recorded result. Exactly one of Quote and
// Unpriced is set: a pin to reserve and settle by, or a model the price table
// has no entry for.
type priceOutcome struct {
	Quote    *catalog.Quote `json:"quote,omitempty"`
	Unpriced bool           `json:"unpriced,omitempty"`
}
