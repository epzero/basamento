package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/schema"

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
	// Tools returns an agent's allowlist. nil means no tools.
	Tools func(agent string) *tool.Set
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

// Handle runs one agent node. input is the node's input; when it carries a
// handoff, it is the previous agent's output and supplies the message history,
// the run's start and the chain's time cap.
func (h *Handler) Handle(dc runtime.DurableContext, node Node, input json.RawMessage) (json.RawMessage, error) {
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
		// 2. The time cap (REQ-AGENT-012), checked every Checks turns.
		if boundary(own, spec.Checks) && timeCap != nil && dc.Now().Sub(startedAt) >= timeCap.Duration() {
			return finish(StateTimeCap, turns, "")
		}
		// 3. The step cap (REQ-AGENT-008).
		if spec.Caps.Steps != nil && own > *spec.Caps.Steps {
			return finish(StateStepCap, turns, "")
		}
		// 4. The memory policy and input token limit (REQ-AGENT-023…027).
		messages, dropped, err := buildMessages(spec.System, turns, spec.InputTokens)
		if err != nil {
			return nil, err
		}
		if h.opts.Observer != nil && dropped > 0 {
			h.opts.Observer.Dropped(own, dropped)
		}
		req := model.Request{
			Model:     spec.Model,
			Op:        model.CapChat,
			Messages:  marshal(messages),
			Tools:     h.toolsJSON(node.Name, spec),
			ClientRef: clientRef(runID, node.Name, own),
		}
		var estimate Estimate
		if h.opts.Estimate != nil {
			estimate, err = h.opts.Estimate(ctx, req)
			if err != nil {
				return nil, err
			}
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

		// Step names carry the node id as well as the turn: a handoff target
		// runs in the same flow run as the agent that handed off, so "turn-1"
		// alone would collide in the runtime's journal. The idempotency key is
		// the step name, stable for the run (design §2).
		stepName := fmt.Sprintf("%s/turn-%d", node.Name, own)
		raw, err := h.runStep(dc, stepName, turnInput{Model: spec.Model, Request: req, Dropped: dropped}, h.runTurn)
		if err != nil {
			if h.opts.Observer != nil {
				h.opts.Observer.Settled(own, 0)
			}
			return nil, err
		}
		var result turnResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, invalid("agent: malformed turn step result: "+err.Error(), map[string]any{"step": stepName})
		}
		used += result.Usage.InputTokens + result.Usage.OutputTokens
		spent += result.CostMpts
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
			name := fmt.Sprintf("%s/tool-%d-%d", node.Name, own, k+1)
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

func finish(state string, turns []Turn, answer string) (json.RawMessage, error) {
	return json.Marshal(Output{State: state, Answer: answer, Turns: turns})
}

// boundary reports whether turn n runs the time-cap and cancellation checks.
func boundary(n, checks int) bool {
	if checks <= 1 {
		return true
	}
	return (n-1)%checks == 0
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
		return nil, err
	}
	if res.State != model.StateSucceeded {
		return nil, core.NewError(core.CodeInternal, "agent: the model invocation did not succeed", map[string]any{"state": string(res.State)})
	}
	if len(res.Message) == 0 {
		return nil, core.NewError(core.CodeInternal, "agent: the model returned no message", nil)
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
	var set *tool.Set
	if h.opts.Tools != nil {
		set = h.opts.Tools(in.Agent)
	}
	outcome, err := h.opts.RunTool(ctx, set, in.Call)
	if err != nil {
		return nil, err
	}
	return json.Marshal(outcome)
}

func (h *Handler) toolsJSON(agent string, spec Spec) json.RawMessage {
	var set *tool.Set
	if h.opts.Tools != nil {
		set = h.opts.Tools(agent)
	}
	if set == nil {
		return nil
	}
	offered := set.Tools
	if len(spec.Tools) > 0 {
		offered = nil
		for _, name := range spec.Tools {
			if t, ok := set.Get(name); ok {
				offered = append(offered, t)
			}
		}
	}
	infos := make([]*schema.ToolInfo, 0, len(offered))
	for _, t := range offered {
		infos = append(infos, &schema.ToolInfo{Name: t.Name, Desc: t.Description})
	}
	if len(infos) == 0 {
		return nil
	}
	return marshal(infos)
}

// splitCalls separates handoff requests, in order, from tool calls.
func splitCalls(calls []schema.ToolCall) ([]schema.ToolCall, []schema.ToolCall) {
	var handoffs []schema.ToolCall
	var tools []schema.ToolCall
	for _, call := range calls {
		if call.Function.Name == "handoff_to" {
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
// (REQ-MODEL-004): a hash of the run, the node and the turn.
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
