package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/tool"
)

// Node is the part of a flow node the agent handler needs. The composition
// root adapts flow.Node to it when it registers the handler with the flow host,
// because package agent may not import package flow (layering.yaml).
type Node struct {
	Name string          // the node's key in the flow's IR
	Spec json.RawMessage // the agent node spec (Spec)
	// Handoffs are the agent node ids this node may hand off to: the targets
	// of its outgoing edges in the flow (design §4). The handler offers the
	// reserved handoff_to tool to the model only when there is at least one.
	Handoffs []string
}

// Estimate is what the loop needs to know about a model turn before it starts:
// the input tokens it will read and the milli-points its price formula
// reserves for that estimated usage (REQ-AGENT-009, REQ-AGENT-011).
type Estimate struct {
	InputTokens int64
	CostMpts    int64
}

// Estimator returns one turn's estimate. It is called before the turn's step,
// so a reserved cost is held before the model is invoked.
type Estimator func(ctx context.Context, req model.Request) (Estimate, error)

// Observer sees the loop's budget and memory decisions. It is optional and
// used by tests and telemetry; it must not change the run.
type Observer interface {
	Reserved(turn int, mpts int64)
	Settled(turn int, charged int64)
	Dropped(turn int, messages int)
}

// Options are the composition root's dependencies of the agent handler.
type Options struct {
	// Models resolves a spec's model id to its invoker.
	Models func(id string) (model.Invoker, error)
	// Estimate reserves a turn's cost and reports its input tokens. nil
	// estimates zero.
	Estimate Estimator
	// RunTool is the tool port (specs/tools/design.md §2). It is required when
	// a model turn requests a tool.
	RunTool tool.Runner
	// Tools resolves an agent's allowlist. The context carries the run's
	// tenant, so one shared handler gives each tenant its own set (REQ-CORE-005).
	// nil means no tools.
	Tools func(ctx context.Context, agent string) (*tool.Set, error)
	// Defines reports whether the flow defines the agent node node. A handoff
	// to a node it rejects fails with not_found (REQ-AGENT-020).
	Defines func(node string) bool
	// Register registers a step function with the runtime before the loop
	// calls dc.Step(name, …). It is optional when the runtime does not need
	// step functions registered ahead of use (the test doubles and the
	// Temporal runtime); the local runtime wants it.
	Register func(name string, fn runtime.StepFunc) error
	// Observer is optional.
	Observer Observer
}

// Handler is the `agent` node handler: one agent run's loop as flow code over
// runtime.DurableContext (ADR-0011). Every model turn and every tool call is a
// separate step, so a resumed run never pays for a completed turn again.
type Handler struct {
	opts Options
}

// New returns a handler over opts.
func New(opts Options) *Handler { return &Handler{opts: opts} }

// Result is what an agent node returns to the flow host: the run's output and
// the cost this node's own turns settled, in milli-points (REQ-FLOW-049). The
// composition root passes CostMpts on as host.NodeResult.CostMpts, so a
// flow.Guard cost bound over an agent node sees the agent's spend.
type Result struct {
	Output   json.RawMessage `json:"output,omitempty"`
	CostMpts int64           `json:"cost_mpts,omitempty"`
}

// Handle runs one agent node and reports what this node settled. input is the
// node's input; when it carries a handoff, it is the previous agent's output
// and supplies the message history, the run's start and the chain's time cap.
func (h *Handler) Handle(dc runtime.DurableContext, node Node, input json.RawMessage) (Result, error) {
	settled := int64(0)
	out, err := h.handle(dc, node, input, &settled)
	return Result{Output: out, CostMpts: settled}, err
}

// handle is Handle's loop. settled receives the run's spend after every
// completed turn, so a run that fails late still reports what it paid for.
func (h *Handler) handle(dc runtime.DurableContext, node Node, input json.RawMessage, settled *int64) (json.RawMessage, error) {
	if dc == nil {
		return nil, invalid("agent: nil durable context", nil)
	}
	spec, err := parseSpec(node.Spec)
	if err != nil {
		return nil, err
	}
	if h.opts.Models == nil {
		return nil, invalid("agent: no model resolver", map[string]any{"model": spec.Model})
	}
	carry, err := parseHandoff(input)
	if err != nil {
		return nil, err
	}
	// A run that starts without a handoff seeds its history with one user
	// message from the node input (REQ-AGENT-031). A handoff target is not
	// seeded again: its history is exactly the carried turns (REQ-AGENT-017).
	var seed *schema.Message
	if carry == nil {
		seed, err = userMessage(input)
		if err != nil {
			return nil, err
		}
	}

	ctx := dc.Context()
	startedAt := dc.Now()
	timeCap := spec.Caps.Time
	var turns []Turn
	if carry != nil {
		turns = append(turns, carry.turns...)
		if carry.hasStart {
			startedAt = carry.startedAt
		}
		if carry.timeCap != nil {
			timeCap = carry.timeCap
		}
	}
	runID := dc.NewID("agent")
	spent, used := int64(0), int64(0)

	for own := 1; ; own++ {
		// 1. Cancellation (REQ-AGENT-028).
		if err := ctx.Err(); err != nil {
			return nil, core.NewError(core.CodeCancelled, "agent: run cancelled", map[string]any{"node": node.Name})
		}
		// 2. The time cap (REQ-AGENT-012), checked before every model turn: a
		// cap that has elapsed is never deferred to a later boundary.
		if timeCap != nil && dc.Now().Sub(startedAt) >= timeCap.Duration() {
			return finish(StateTimeCap, turns, "")
		}
		// 3. The step cap (REQ-AGENT-008).
		if spec.Caps.Steps != nil && own > *spec.Caps.Steps {
			return finish(StateStepCap, turns, "")
		}
		// 4. The memory policy and input token limit (REQ-AGENT-023…027).
		messages, dropped, err := buildMessages(spec.System, seed, turns, spec.InputTokens)
		if err != nil {
			return nil, err
		}
		// REQ-AGENT-032: a request with no non-system message is never sent.
		// The backstop runs after the memory policy (so a system message over
		// the limit is reported as such) and before any reservation or
		// invocation.
		if !hasNonSystem(messages) {
			return nil, invalid(fmt.Sprintf("agent %q: the run has no user message", node.Name), nil)
		}
		if h.opts.Observer != nil && dropped > 0 {
			h.opts.Observer.Dropped(own, dropped)
		}
		tools, err := h.toolsJSON(ctx, node.Name, spec, node.Handoffs)
		if err != nil {
			return nil, err
		}
		req := model.Request{
			Model:     spec.Model,
			Op:        model.CapChat,
			Messages:  marshal(messages),
			Tools:     tools,
			ClientRef: clientRef(runID, node.Name, own),
		}
		var estimate Estimate
		if h.opts.Estimate != nil {
			estimate, err = h.opts.Estimate(ctx, req)
			if err != nil {
				return nil, err
			}
		}
		// The post-build guarantee of REQ-AGENT-027: the estimator measures
		// the request as assembled, so tool definitions and tool results it
		// may count beyond the recorded usage still fail before the invoke.
		if spec.InputTokens > 0 && estimate.InputTokens > spec.InputTokens {
			return nil, invalid("agent: the assembled request exceeds the input token limit", map[string]any{
				"input_tokens": spec.InputTokens, "used": estimate.InputTokens,
			})
		}
		// 5. The token cap (REQ-AGENT-011).
		if spec.Caps.Tokens != nil && used+estimate.InputTokens > *spec.Caps.Tokens {
			return finish(StateTokenCap, turns, "")
		}
		// 6. The cost cap and reservation (REQ-AGENT-009, REQ-AGENT-010).
		if spec.Caps.CostMpts != nil && spent+estimate.CostMpts > *spec.Caps.CostMpts {
			return finish(StateCostCap, turns, "")
		}
		if h.opts.Observer != nil {
			h.opts.Observer.Reserved(own, estimate.CostMpts)
		}

		// Step names carry the node id and this visit's run id as well as the
		// turn: a handoff target runs in the same flow run as the agent that
		// handed off, and a flow may visit the same agent node twice (a cycle
		// or a handoff back). Without the run id, "node/turn-1" would replay
		// the first visit's recorded turn instead of invoking the model again.
		// runID comes from dc.NewID, so it is deterministic per run and
		// survives a resume (design §2, REQ-AGENT-002).
		stepName := fmt.Sprintf("%s/%s/turn-%d", node.Name, runID, own)
		raw, err := h.runStep(dc, stepName, turnInput{Model: spec.Model, Request: req, Dropped: dropped}, h.runTurn)
		if err != nil {
			// A turn that failed still settles what its invocation charged, so
			// the host journals node_failed with that cost (REQ-FLOW-049).
			charged := int64(0)
			var failed *turnError
			if errors.As(err, &failed) {
				charged = failed.cost
				*settled += charged
			}
			if h.opts.Observer != nil {
				h.opts.Observer.Settled(own, charged)
			}
			return nil, err
		}
		var result turnResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, invalid("agent: malformed turn step result: "+err.Error(), map[string]any{"step": stepName})
		}
		used += result.Usage.InputTokens + result.Usage.OutputTokens
		spent += result.CostMpts
		*settled = spent
		if h.opts.Observer != nil {
			h.opts.Observer.Settled(own, result.CostMpts)
		}

		var reply schema.Message
		if err := json.Unmarshal(result.Message, &reply); err != nil {
			return nil, invalid("agent: model reply is not a message: "+err.Error(), map[string]any{"step": stepName})
		}
		turns = append(turns, Turn{
			Message: result.Message,
			Tokens:  result.Usage.InputTokens + result.Usage.OutputTokens,
			Dropped: result.Dropped,
		})

		handoffs, calls := splitCalls(reply.ToolCalls)
		if len(handoffs) > 1 {
			return nil, invalid("agent: a model turn requested more than one handoff", map[string]any{
				"node": node.Name, "handoffs": len(handoffs),
			})
		}
		if len(handoffs) == 1 {
			target, err := handoffTarget(handoffs[0])
			if err != nil {
				return nil, err
			}
			if h.opts.Defines == nil || !h.opts.Defines(target) {
				return nil, core.NewError(core.CodeNotFound,
					fmt.Sprintf("agent %q: the flow does not define it", target),
					map[string]any{"agent": target, "node": node.Name})
			}
			out := handoff(target, turns, startedAt, timeCapCopy(timeCap))
			return json.Marshal(out)
		}
		if len(calls) == 0 {
			return finish(StateCompleted, turns, reply.Content)
		}

		for k, call := range calls {
			name := fmt.Sprintf("%s/%s/tool-%d-%d", node.Name, runID, own, k+1)
			in := toolStepInput{Agent: node.Name, ID: call.ID, Call: tool.Call{Name: call.Function.Name, Args: json.RawMessage(call.Function.Arguments)}}
			raw, err := h.runStep(dc, name, in, h.runTool)
			if err != nil {
				return nil, err
			}
			var outcome tool.Outcome
			if err := json.Unmarshal(raw, &outcome); err != nil {
				return nil, invalid("agent: malformed tool step result: "+err.Error(), map[string]any{"step": name})
			}
			if outcome.Failed {
				code := outcome.Code
				if code == "" {
					code = core.CodeInternal
				}
				return nil, core.NewError(code, outcome.Message, map[string]any{"tool": call.Function.Name})
			}
			turns[len(turns)-1].Results = append(turns[len(turns)-1].Results, toolMessage(call, outcome))
		}
	}
}

// userMessage builds a run's seed user message from the node input
// (REQ-AGENT-031): a JSON string contributes its decoded text, any other
// non-empty JSON its compact bytes, and empty or whitespace-only input no
// message at all. The seed is the run's first turn for the memory policy, so it
// is never dropped and is weighted by its content estimate.
func userMessage(input json.RawMessage) (*schema.Message, error) {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		return &schema.Message{Role: schema.User, Content: text}, nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return nil, invalid("agent: node input is not JSON: "+err.Error(), nil)
	}
	return &schema.Message{Role: schema.User, Content: compact.String()}, nil
}

// hasNonSystem reports whether the assembled request carries a message the
// model can answer: a user message or a model turn, not only system ones.
func hasNonSystem(messages []*schema.Message) bool {
	for _, m := range messages {
		if m.Role != schema.System {
			return true
		}
	}
	return false
}

func finish(state string, turns []Turn, answer string) (json.RawMessage, error) {
	return json.Marshal(Output{State: state, Answer: answer, Turns: turns})
}

func (h *Handler) runStep(dc runtime.DurableContext, name string, in any, fn runtime.StepFunc) (json.RawMessage, error) {
	if h.opts.Register != nil {
		if err := h.opts.Register(name, fn); err != nil && core.CodeOf(err) != core.CodeConflict {
			return nil, err
		}
	}
	var raw json.RawMessage
	if err := dc.Step(name, in, runtime.StepOptions{IdempotencyKey: name}).Get(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

type turnInput struct {
	Model   string        `json:"model"`
	Request model.Request `json:"request"`
	Dropped int           `json:"dropped"`
}

// turnResult is the durable result of one model turn: the model output as a
// message, its usage and charge, and the messages the memory policy dropped
// (specs/agents/design.md §5).
type turnResult struct {
	Type     string          `json:"type"`
	Message  json.RawMessage `json:"message"`
	Usage    model.Usage     `json:"usage"`
	CostMpts int64           `json:"cost_mpts"`
	Dropped  int             `json:"dropped"`
}

func (h *Handler) runTurn(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var in turnInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, invalid("agent: malformed turn step input: "+err.Error(), nil)
	}
	if h.opts.Models == nil {
		return nil, invalid("agent: no model resolver", map[string]any{"model": in.Model})
	}
	invoker, err := h.opts.Models(in.Model)
	if err != nil {
		return nil, err
	}
	res, err := model.Invoke(ctx, invoker, in.Request)
	if err != nil {
		return nil, turnFailed(res, err)
	}
	if res.State != model.StateSucceeded {
		return nil, turnFailed(res, core.NewError(core.CodeInternal, "agent: the model invocation did not succeed", map[string]any{"state": string(res.State)}))
	}
	if len(res.Message) == 0 {
		return nil, turnFailed(res, core.NewError(core.CodeInternal, "agent: the model returned no message", nil))
	}
	usage := model.Usage{}
	if res.Usage != nil {
		usage = *res.Usage
	}
	return json.Marshal(turnResult{
		Type:     "message.completed",
		Message:  res.Message,
		Usage:    usage,
		CostMpts: res.CostMpts,
		Dropped:  in.Dropped,
	})
}

// turnFailed attaches the cost a failed model invocation was charged to its
// error, so the loop can settle it before returning the failure.
func turnFailed(res model.Result, err error) error {
	if res.CostMpts == 0 {
		return err
	}
	return &turnError{cost: res.CostMpts, err: err}
}

// turnError is a failed model turn whose invocation still cost milli-points.
type turnError struct {
	cost int64
	err  error
}

func (e *turnError) Error() string { return e.err.Error() }
func (e *turnError) Unwrap() error { return e.err }

type toolStepInput struct {
	Agent string    `json:"agent"`
	ID    string    `json:"id,omitempty"`
	Call  tool.Call `json:"call"`
}

func (h *Handler) runTool(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var in toolStepInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, invalid("agent: malformed tool step input: "+err.Error(), nil)
	}
	if h.opts.RunTool == nil {
		return nil, core.NewError(core.CodeUnsupported, "agent: no tool runner", map[string]any{"tool": in.Call.Name})
	}
	set, err := h.toolSet(ctx, in.Agent)
	if err != nil {
		return nil, err
	}
	outcome, err := h.opts.RunTool(ctx, set, in.Call)
	if err != nil {
		return nil, err
	}
	return json.Marshal(outcome)
}

// toolSet resolves the set of tools the run's tenant may use.
func (h *Handler) toolSet(ctx context.Context, agent string) (*tool.Set, error) {
	if h.opts.Tools == nil {
		return nil, nil
	}
	return h.opts.Tools(ctx, agent)
}

// toolsJSON renders the tools offered to the model. Each tool's Input JSON
// Schema becomes its ParamsOneOf: eino reads an absent ParamsOneOf as a tool
// that takes no arguments, which would hide a required argument from the model.
func (h *Handler) toolsJSON(ctx context.Context, agent string, spec Spec, handoffs []string) (json.RawMessage, error) {
	set, err := h.toolSet(ctx, agent)
	if err != nil {
		return nil, err
	}
	var infos []*schema.ToolInfo
	if set != nil {
		offered := set.Tools
		if len(spec.Tools) > 0 {
			offered = nil
			for _, name := range spec.Tools {
				if t, ok := set.Get(name); ok {
					offered = append(offered, t)
				}
			}
		}
		for _, t := range offered {
			if t.Name == handoffTool {
				continue // reserved: the loop intercepts it, the tool port never runs it
			}
			info := &schema.ToolInfo{Name: t.Name, Desc: t.Description}
			if len(bytes.TrimSpace(t.Input)) > 0 {
				var input jsonschema.Schema
				if err := json.Unmarshal(t.Input, &input); err != nil {
					return nil, invalid(fmt.Sprintf("agent: tool %q has an invalid input schema: %s", t.Name, err), map[string]any{"tool": t.Name})
				}
				info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(&input)
			}
			infos = append(infos, info)
		}
	}
	// A node with handoff targets offers the reserved handoff_to tool, so a
	// real model can request a handoff the loop then intercepts (REQ-AGENT-016,
	// design §4). It is never resolved through the tool port.
	if len(handoffs) > 0 {
		infos = append(infos, handoffInfo(handoffs))
	}
	if len(infos) == 0 {
		return nil, nil
	}
	return marshal(infos), nil
}

// handoffTool is the reserved tool name a model uses to request a handoff.
const handoffTool = "handoff_to"

// handoffInfo is the schema of the reserved handoff tool: the target is one of
// the node's handoff targets, the agent node ids its outgoing edges reach.
func handoffInfo(targets []string) *schema.ToolInfo {
	enum := make([]any, len(targets))
	for i, target := range targets {
		enum[i] = target
	}
	input := &jsonschema.Schema{
		Type:       "object",
		Properties: jsonschema.NewProperties(),
		Required:   []string{"target"},
	}
	input.Properties.Set("target", &jsonschema.Schema{
		Type:        "string",
		Enum:        enum,
		Description: "the agent node to hand off to",
	})
	return &schema.ToolInfo{
		Name:        handoffTool,
		Desc:        "End this agent run and hand its turns to another agent node.",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(input),
	}
}

// splitCalls separates handoff requests, in order, from tool calls.
func splitCalls(calls []schema.ToolCall) ([]schema.ToolCall, []schema.ToolCall) {
	var handoffs []schema.ToolCall
	var tools []schema.ToolCall
	for _, call := range calls {
		if call.Function.Name == handoffTool {
			handoffs = append(handoffs, call)
			continue
		}
		tools = append(tools, call)
	}
	return handoffs, tools
}

// handoffTarget reads the node id from a handoff_to call: a JSON string, or
// an object with target, agent or node.
func handoffTarget(call schema.ToolCall) (string, error) {
	args := call.Function.Arguments
	var target string
	if err := json.Unmarshal([]byte(args), &target); err == nil && target != "" {
		return target, nil
	}
	var obj struct {
		Target string `json:"target"`
		Agent  string `json:"agent"`
		Node   string `json:"node"`
	}
	if err := json.Unmarshal([]byte(args), &obj); err != nil {
		return "", invalid("agent: a handoff_to call has invalid arguments: "+err.Error(), nil)
	}
	for _, candidate := range []string{obj.Target, obj.Agent, obj.Node} {
		if candidate != "" {
			return candidate, nil
		}
	}
	return "", invalid("agent: a handoff_to call names no target", nil)
}

func toolMessage(call schema.ToolCall, outcome tool.Outcome) json.RawMessage {
	return marshal(&schema.Message{
		Role:       schema.Tool,
		Content:    outcome.Text(),
		ToolCallID: call.ID,
		ToolName:   call.Function.Name,
	})
}

func timeCapCopy(d *Duration) *Duration {
	if d == nil {
		return nil
	}
	c := *d
	return &c
}

// clientRef is the turn's stable idempotency key across retries
// (REQ-MODEL-004): a hash of the visit's run id, the node and the turn. runID
// is unique per visit, so a second visit to the same node never reuses the
// first visit's key.
func clientRef(runID, node string, turn int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00turn-%d", runID, node, turn)))
	return hex.EncodeToString(sum[:])
}

func marshal(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic("agent: marshal: " + err.Error())
	}
	return raw
}
