package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/flow/machine"
	"github.com/epzero/basamento/runtime"
)

// NodeResult is what a NodeHandler returns for one node: its output and the cost
// the node settled, in milli-points (REQ-FLOW-049). Step, gate, wait, subflow and
// emit nodes settle no cost through the host today; CostMpts stays zero for them.
// A negative CostMpts is a programming error of the handler: the machine rejects
// the event and the run fails with code invalid_argument.
type NodeResult struct {
	Output   json.RawMessage // the node's output update
	CostMpts int64           // settled cost; non-negative; zero when the node cost nothing
}

// NodeHandler is the flow code of one effectful node kind (ADR-0011). It runs
// deterministic flow code; its own side effects are DurableContext calls,
// usually Step. The host calls it directly, one command at a time.
type NodeHandler func(dc runtime.DurableContext, node flow.Node, input json.RawMessage) (NodeResult, error)

// DefinitionCatalog resolves a flow reference to its compiled IR (WP-L4-01).
// Loading is not a durable step: the host loads once at the start of a run and
// keeps the definition for that execution (REQ-FLOW-027).
type DefinitionCatalog interface {
	Load(ctx context.Context, ref runtime.FlowRef) (*flow.IR, error)
}

// Host implements runtime.FlowHost: it loads the pinned definition, drives the
// pure machine and turns its commands into DurableContext calls. A Host is
// read-only after the composition root has registered its handlers, so one
// Host may serve concurrent runs.
type Host struct {
	catalog     DefinitionCatalog
	handlers    map[string]NodeHandler
	machineOpts []machine.Option
}

// New returns a host over catalog. Register every node handler before the host
// is registered with a runtime.
func New(catalog DefinitionCatalog, opts ...Option) *Host {
	h := &Host{catalog: catalog, handlers: map[string]NodeHandler{}}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// RegisterHandler registers handler for a node kind. Calling it again for the
// same kind replaces the handler; the composition root does it before the host
// is used. A nil handler removes the registration for kind. RegisterHandler is
// not safe to call concurrently with Run: the handler table is a plain map
// written here and read during runs once registration has finished. The host
// itself imports no handler (ADR-0011).
func (h *Host) RegisterHandler(kind string, handler NodeHandler) {
	if h.handlers == nil {
		h.handlers = map[string]NodeHandler{}
	}
	if handler == nil {
		delete(h.handlers, kind)
		return
	}
	h.handlers[kind] = handler
}

// Run implements runtime.FlowHost. It loads the IR pinned by ref, checks that
// every effectful node kind has a handler, then drives flow/machine until it
// completes or fails.
//
// The host keeps no state between calls: the snapshot it carries is the
// machine's, and every durable result comes from dc, which is replay-safe
// (REQ-FLOW-037, REQ-FLOW-038). After loading, a non-empty ref.Hash must
// match ir.Hash of the definition. Idempotency keys include the superstep
// sequence. When several futures in one superstep fail, the first one dc.Select
// reports ends the run; other futures are not awaited. A failed node handler
// ends the superstep: later handlers in the batch do not run.
func (h *Host) Run(dc runtime.DurableContext, ref runtime.FlowRef, input json.RawMessage) (json.RawMessage, error) {
	if dc == nil {
		return nil, invalid("flow host: nil durable context", nil)
	}
	if h == nil || h.catalog == nil {
		return nil, invalid("flow host: no definition catalog", nil)
	}
	def, err := h.catalog.Load(dc.Context(), ref)
	if err != nil {
		return nil, err
	}
	if def == nil {
		return nil, core.NewError(core.CodeNotFound, fmt.Sprintf("flow %q: definition not found", ref.Name), map[string]any{"flow": ref.Name, "hash": ref.Hash})
	}
	if ref.Hash != "" {
		loaded, err := ir.Hash(def)
		if err != nil {
			return nil, err
		}
		if loaded != ref.Hash {
			return nil, core.NewError(core.CodeInternal, "flow host: loaded definition does not match the pinned hash",
				map[string]any{"flow": ref.Name, "pinned_hash": ref.Hash, "loaded_hash": loaded})
		}
	}
	if err := h.checkHandlers(def); err != nil {
		return nil, err
	}
	m, err := machine.New(def, h.machineOpts...)
	if err != nil {
		return nil, err
	}
	snapshot, commands, err := m.Start(input)
	if err != nil {
		return nil, err
	}
	for {
		if len(commands) == 0 {
			return nil, core.NewError(core.CodeInternal, "flow host: machine returned no command", map[string]any{"flow": def.Name})
		}
		if err := runContextError(dc.Context()); err != nil {
			return nil, err
		}
		outcome, err := h.superstep(dc, m, def, ref, &snapshot, commands)
		if err != nil {
			return nil, err
		}
		if outcome.done {
			return outcome.state, outcome.err
		}
		commands = outcome.next
	}
}

// checkHandlers rejects the whole run before any node when an effectful kind
// has no registered handler (REQ-FLOW-028). Nodes are scanned in sorted order
// so two missing handlers always report the same node.
func (h *Host) checkHandlers(def *flow.IR) error {
	for _, id := range sortedNodeIDs(def) {
		node := def.Nodes[id]
		if node.Kind != "model" && node.Kind != "agent" {
			continue
		}
		if _, ok := h.handlers[node.Kind]; ok {
			continue
		}
		return core.NewError(core.CodeUnsupported,
			fmt.Sprintf("flow: node %q: no node handler is registered for kind %q", id, node.Kind),
			map[string]any{"node": id, "kind": node.Kind})
	}
	return nil
}

// pendingCall is one Future-returning durable operation of a superstep and the
// event its result feeds back to the machine.
type pendingCall struct {
	command flow.Command
	future  runtime.Future
	event   string
}

type outcome struct {
	done  bool
	state json.RawMessage
	err   error
	next  []flow.Command
}

// superstep executes one batch of commands. All Future-returning durable calls
// are started in command order before the host waits for any of them
// (REQ-FLOW-044). Node handlers are synchronous: they are called in command
// order after the batch has started, and their events are fed to the machine
// before the host waits on the futures.
func (h *Host) superstep(dc runtime.DurableContext, m *machine.Machine, def *flow.IR, ref runtime.FlowRef, snapshot *flow.Snapshot, commands []flow.Command) (outcome, error) {
	pending, handlers, end, err := h.startBatch(dc, def, ref, snapshot, commands)
	if err != nil || end != nil {
		if end != nil {
			return *end, nil
		}
		return outcome{}, err
	}
	events, err := h.runHandlers(dc, def, handlers)
	if err != nil {
		return outcome{}, err
	}
	next, end, err := feedEvents(m, snapshot, events)
	if err != nil {
		return outcome{}, err
	}
	if end != nil {
		return *end, nil
	}
	if len(next) > 0 {
		if len(pending) > 0 {
			return outcome{}, core.NewError(core.CodeInternal, "flow host: machine advanced before every step of the superstep reported", nil)
		}
		return outcome{next: next}, nil
	}
	return h.await(dc, m, snapshot, pending)
}

// startBatch starts the Future-returning calls of one superstep in command
// order and collects the handler commands for after the batch. It returns the
// terminal command, if the batch contains one.
func (h *Host) startBatch(dc runtime.DurableContext, def *flow.IR, ref runtime.FlowRef, snapshot *flow.Snapshot, commands []flow.Command) ([]pendingCall, []flow.Command, *outcome, error) {
	var pending []pendingCall
	var handlers []flow.Command
	for _, command := range commands {
		switch command.Type {
		case "complete":
			return nil, nil, &outcome{done: true, state: snapshot.State}, nil
		case "fail":
			return nil, nil, &outcome{done: true, err: commandError(command)}, nil
		case "emit":
			if err := dc.Emit(runtime.ProgressEvent{Type: emitType(command), Data: command.Input}); err != nil {
				return nil, nil, nil, err
			}
		case "checkpoint":
			blob := string(bytes.TrimSpace(command.Input))
			if err := dc.Checkpoint(runtime.SnapshotRef{BlobKey: blob, Seq: snapshot.Seq}); err != nil {
				return nil, nil, nil, err
			}
		case "start_timer":
			node, err := lookupNode(def, command.Node)
			if err != nil {
				return nil, nil, nil, err
			}
			duration, err := durationOf(node, command.Node)
			if err != nil {
				return nil, nil, nil, err
			}
			pending = append(pending, pendingCall{
				command: command,
				future:  dc.Sleep(duration),
				event:   "timer_fired",
			})
		case "await_gate":
			node, err := lookupNode(def, command.Node)
			if err != nil {
				return nil, nil, nil, err
			}
			spec, err := gateSpecOf(node, command.Node)
			if err != nil {
				return nil, nil, nil, err
			}
			pending = append(pending, pendingCall{
				command: command,
				future:  dc.Await(spec),
				event:   "gate_resolved",
			})
		case "run_node":
			node, err := lookupNode(def, command.Node)
			if err != nil {
				return nil, nil, nil, err
			}
			switch node.Kind {
			case "tool", "task", "subflow":
				call, err := h.runNode(dc, def, ref, snapshot, command, node)
				if err != nil {
					return nil, nil, nil, err
				}
				pending = append(pending, call)
			default:
				// A kind with a registered handler runs after every future in
				// the batch has started: the handler is synchronous flow code
				// and may block on its own steps.
				handlers = append(handlers, command)
			}
		default:
			return nil, nil, nil, invalid(fmt.Sprintf("flow host: unknown command %q", command.Type), map[string]any{"command": command.Type})
		}
	}
	return pending, handlers, nil, nil
}

// runHandlers calls the node handlers of one superstep in command order and
// turns their results into the events the machine expects.
func (h *Host) runHandlers(dc runtime.DurableContext, def *flow.IR, commands []flow.Command) ([]flow.Event, error) {
	events := make([]flow.Event, 0, len(commands))
	for _, command := range commands {
		if err := runContextError(dc.Context()); err != nil {
			return nil, err
		}
		node, err := lookupNode(def, command.Node)
		if err != nil {
			return nil, err
		}
		handler, ok := h.handlers[node.Kind]
		if !ok {
			return nil, core.NewError(core.CodeUnsupported,
				fmt.Sprintf("flow: node %q: no node handler is registered for kind %q", command.Node, node.Kind),
				map[string]any{"node": command.Node, "kind": node.Kind})
		}
		node.Name = command.Node
		result, err := handler(dc, node, command.Input)
		if err != nil {
			events = append(events, failedEvent(command.Node, command.Attempt, err, result.CostMpts))
			return events, nil
		}
		events = append(events, flow.Event{
			Type:     "node_completed",
			Node:     command.Node,
			Attempt:  command.Attempt,
			Payload:  result.Output,
			CostMpts: result.CostMpts,
		})
	}
	return events, nil
}

// await waits for the superstep's futures with Select, feeding each result
// back as the event its command defines. A failed future becomes node_failed
// (REQ-FLOW-035).
func (h *Host) await(dc runtime.DurableContext, m *machine.Machine, snapshot *flow.Snapshot, pending []pendingCall) (outcome, error) {
	for len(pending) > 0 {
		if err := runContextError(dc.Context()); err != nil {
			return outcome{}, err
		}
		futures := make([]runtime.Future, len(pending))
		for i := range pending {
			futures[i] = pending[i].future
		}
		index, err := dc.Select(futures...)
		if err != nil {
			return outcome{}, err
		}
		if index < 0 || index >= len(pending) {
			return outcome{}, core.NewError(core.CodeInternal, fmt.Sprintf("flow host: Select returned index %d for %d futures", index, len(pending)), nil)
		}
		call := pending[index]
		pending = append(pending[:index], pending[index+1:]...)

		var payload json.RawMessage
		err = call.future.Get(&payload)
		event := flow.Event{
			Type:    call.event,
			Node:    call.command.Node,
			Attempt: call.command.Attempt,
		}
		if err != nil {
			// A timer or gate that fails (timeout, cancellation) is a failed
			// node like any other.
			event = failedEvent(call.command.Node, call.command.Attempt, err, 0)
		} else {
			event.Payload = payload
		}
		next, end, err := feedEvents(m, snapshot, []flow.Event{event})
		if err != nil {
			return outcome{}, err
		}
		if end != nil {
			return *end, nil
		}
		if len(next) > 0 {
			if len(pending) > 0 {
				return outcome{}, core.NewError(core.CodeInternal, "flow host: machine advanced before every step of the superstep reported", nil)
			}
			return outcome{next: next}, nil
		}
	}
	return outcome{}, core.NewError(core.CodeInternal, "flow host: superstep produced no next command", nil)
}

// feedEvents applies events to the machine and returns the next superstep's
// commands, or the run's terminal outcome.
func feedEvents(m *machine.Machine, snapshot *flow.Snapshot, events []flow.Event) ([]flow.Command, *outcome, error) {
	var next []flow.Command
	for _, event := range events {
		var end *outcome
		var err error
		next, end, err = apply(m, snapshot, event)
		if err != nil {
			return nil, nil, err
		}
		if end != nil {
			return nil, end, nil
		}
	}
	return next, nil, nil
}

// runNode starts the Future-returning durable call for a run_node command: a
// step for a tool or task node, a child run for a subflow node. Handler kinds
// are run by superstep after the batch has started.
func (h *Host) runNode(dc runtime.DurableContext, def *flow.IR, ref runtime.FlowRef, snapshot *flow.Snapshot, command flow.Command, node flow.Node) (pendingCall, error) {
	switch node.Kind {
	case "tool", "task":
		name, err := nameOf(node, command.Node)
		if err != nil {
			return pendingCall{}, err
		}
		return pendingCall{
			command: command,
			future: dc.Step(name, jsonArgument(command.Input), runtime.StepOptions{
				IdempotencyKey: idempotencyKey(def, ref, snapshot, command),
			}),
			event: "node_completed",
		}, nil
	case "subflow":
		child, err := subflowOf(node, command.Node)
		if err != nil {
			return pendingCall{}, err
		}
		return pendingCall{
			command: command,
			// REQ-RT-015: terminate — only this run needs the child output; a still-running
			// child becomes terminated when the parent closes.
			future: dc.Child(child, jsonArgument(command.Input), runtime.ChildOptions{ID: dc.NewID("subflow"), ParentClose: "terminate"}),
			event:  "node_completed",
		}, nil
	default:
		return pendingCall{}, core.NewError(core.CodeUnsupported,
			fmt.Sprintf("flow: node %q: kind %q does not run as a durable call", command.Node, node.Kind),
			map[string]any{"node": command.Node, "kind": node.Kind})
	}
}

// apply feeds one event to the machine and turns a terminal command into an
// outcome. A non-terminal next command is returned to the caller.
func apply(m *machine.Machine, snapshot *flow.Snapshot, event flow.Event) ([]flow.Command, *outcome, error) {
	next, commands, err := m.Apply(*snapshot, event)
	if err != nil {
		return nil, nil, err
	}
	*snapshot = next
	if len(commands) == 0 {
		return nil, nil, nil
	}
	switch commands[0].Type {
	case "complete":
		return nil, &outcome{done: true, state: next.State}, nil
	case "fail":
		return nil, &outcome{done: true, err: commandError(commands[0])}, nil
	default:
		return commands, nil, nil
	}
}

func failedEvent(node string, attempt int, err error, costMpts int64) flow.Event {
	code, message := nodeFailurePayload(node, err)
	payload, _ := json.Marshal(map[string]any{
		"code":    string(code),
		"message": message,
	})
	return flow.Event{
		Type:     "node_failed",
		Node:     node,
		Attempt:  attempt,
		Payload:  payload,
		CostMpts: costMpts,
	}
}

func nodeFailurePayload(node string, err error) (core.Code, string) {
	var pe *core.Error
	if errors.As(err, &pe) {
		return pe.Code, pe.Message
	}
	return core.CodeInternal, fmt.Sprintf(`flow: node "%s" failed`, node)
}

// commandError turns the machine's fail payload into the run's platform error.
func commandError(command flow.Command) error {
	var payload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if len(bytes.TrimSpace(command.Input)) > 0 {
		_ = json.Unmarshal(command.Input, &payload)
	}
	code := core.Code(payload.Code)
	if code == "" {
		code = core.CodeInternal
	}
	message := payload.Message
	if message == "" {
		message = fmt.Sprintf("flow: run failed at node %q", command.Node)
	}
	return core.NewError(code, message, map[string]any{"node": command.Node})
}

func emitType(command flow.Command) string {
	if command.Node != "" {
		return command.Node
	}
	return "flow.event"
}

func idempotencyKey(def *flow.IR, ref runtime.FlowRef, snapshot *flow.Snapshot, command flow.Command) string {
	var seq int64
	if snapshot != nil {
		seq = snapshot.Seq
	}
	return fmt.Sprintf("%s@%s/%s#%d@%d", def.Name, ref.Hash, command.Node, command.Attempt, seq)
}
