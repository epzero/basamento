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
	// Blobs, when set, stores output files ingested with provenance
	// (REQ-MODEL-008).
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
// code. The reservation is settled on every path after it was taken.
func (h *Handler) Handle(dc runtime.DurableContext, cfg Config, input json.RawMessage) (Output, error) {
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

	ref := ClientRef(dc.NewID(visitToken), cfg.Name, refInput(input))
	req := request(cfg, in, ref)

	// Pricing exists to reserve and settle. A metered run must be priced: a
	// model with no price in any layer fails with unsupported (REQ-MODEL-073).
	// An unmetered run is priced when it can be, for the usage-based
	// settlement below, but an unpriced model is not an error there: it has
	// nothing to reserve.
	var quote catalog.Quote
	priced := false
	budget, metered := BudgetFrom(dc.Context())
	quote, err = h.deps.Catalog.Start(dc.Context(), catalog.Request{
		Model:       cfg.Model,
		Params:      in.Params,
		InputTokens: inputTokens(cfg, in),
		OutputLimit: outputLimit(cfg, in),
	})
	if err != nil {
		if metered || core.CodeOf(err) != core.CodeUnsupported {
			return Output{}, err
		}
	} else {
		priced = true
	}

	out := Output{ClientRef: ref}
	var charged int64
	reserved := false
	if metered {
		if err := budget.Reserve(ref, quote.Estimate); err != nil {
			return Output{}, err
		}
		reserved = true
		out.ReservedMpts = quote.Estimate
	}
	settled := false
	// Every path after the reservation settles it: success charges the actual
	// cost, a failure, an ambiguity or a cancellation releases what is left
	// (REQ-MODEL-006, REQ-MODEL-009).
	defer func() {
		if reserved && !settled {
			_ = budget.Settle(ref, charged)
		}
	}()
	settle := func(actual int64) {
		charged = actual
		out.CostMpts = actual
		if reserved && !settled {
			_ = budget.Settle(ref, actual)
			settled = true
		}
	}

	started, err := h.startInvocation(dc, cfg, req, ref)
	if err != nil {
		return Output{}, err
	}
	if started.Ambiguous {
		handle, err := h.resolveAmbiguous(dc, cfg, ref)
		if err != nil {
			return Output{}, err
		}
		started.Handle = handle
	}

	followed, err := h.followInvocation(dc, cfg, started.Handle, ref)
	if err != nil {
		return Output{}, err
	}

	// Settle before anything that can fail after the invocation ended: the
	// provider already ran and may have charged. Progress deltas are
	// best-effort and never fail a paid invocation that already settled.
	result := followed.Result
	actual, err := h.actualCost(result, quote, priced)
	if err != nil {
		settle(0)
		return Output{}, err
	}
	settle(actual)

	out.State = result.State
	out.Message = result.Message
	out.Vectors = result.Vectors
	out.Usage = result.Usage

	for _, event := range followed.Events {
		data, err := json.Marshal(event)
		if err != nil {
			continue
		}
		_ = dc.Emit(runtime.ProgressEvent{Type: "model.delta", Data: data})
	}
	if len(result.Outputs) > 0 && h.deps.Blobs != nil {
		assets, err := h.ingestOutputs(dc, cfg, req, result, actual, ref)
		if err != nil {
			return out, err
		}
		out.Assets = assets
	}
	if followed.Cancelled {
		return out, core.NewError(core.CodeCancelled, "model node: invocation cancelled with the run", nil)
	}
	if result.State == model.StateFailed {
		return out, failureError(result)
	}
	return out, nil
}

// startInvocation runs the start step.
func (h *Handler) startInvocation(dc runtime.DurableContext, cfg Config, req model.Request, ref string) (StartOutcome, error) {
	name := stepName(cfg, "start", ref)
	if err := h.register(name, h.startStep); err != nil {
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
	if payload.Handle != nil && payload.Handle.ClientRef != "" {
		return *payload.Handle, nil
	}
	name := stepName(cfg, "lookup", ref)
	if err := h.register(name, h.lookupStep); err != nil {
		return model.Handle{}, err
	}
	var out lookupOutcome
	if err := dc.Step(name, lookupIn{ClientRef: ref}, runtime.StepOptions{IdempotencyKey: name}).Get(&out); err != nil {
		return model.Handle{}, core.NewError(core.CodeAmbiguousStart,
			fmt.Sprintf("model node %q: the start of %q cannot be resolved", cfg.Name, ref),
			map[string]any{"node": cfg.Name, "client_ref": ref, "cause": err.Error()})
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
	if err := h.register(name, h.followStep); err != nil {
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
	var collected []model.Event
	for event := range events {
		collected = append(collected, event)
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
		return json.Marshal(followOutcome{Events: collected, Result: result, Cancelled: true})
	}
	result, err := h.deps.Invoker.Result(ctx, in.Handle)
	if err != nil {
		return nil, model.MapError(err)
	}
	return json.Marshal(followOutcome{Events: collected, Result: result}) // normal
}

// actualCost is what the run is charged: the cost the provider reports, or
// else the price formula on the actual usage (docs/architecture/model-layer.md
// §7).
func (h *Handler) actualCost(result model.Result, quote catalog.Quote, priced bool) (int64, error) {
	if result.CostMpts > 0 {
		return result.CostMpts, nil
	}
	if result.Usage == nil || !priced {
		return 0, nil
	}
	charge, err := h.deps.Catalog.Settle(quote, *result.Usage)
	if err != nil {
		return 0, err
	}
	return charge.MilliPoints, nil
}

// register installs one of the handler's steps with the runtime, once.
func (h *Handler) register(name string, fn runtime.StepFunc) error {
	if h.deps.Steps == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.registered[name] {
		return nil
	}
	err := h.deps.Steps.RegisterStep(name, fn, runtime.StepDefaults{Options: runtime.StepOptions{Heartbeat: stepHeartbeat}})
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
	Events    []model.Event `json:"events,omitempty"`
	Result    model.Result  `json:"result"`
	Cancelled bool          `json:"cancelled,omitempty"`
}
