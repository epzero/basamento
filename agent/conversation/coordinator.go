package conversation

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// Turns is the control port of the running turn's child run. The coordinator
// delivers steering at the next step boundary and cancels a turn through it;
// the composition root implements it over the runtime's typed input channel
// (design §3, §6).
type Turns interface {
	// Steer delivers text to the running child at its next step boundary.
	Steer(ctx context.Context, child runtime.RunRef, text string) error
	// Cancel cancels the running child run.
	Cancel(ctx context.Context, child runtime.RunRef, reason string) error
	// Partial returns the model messages the running turn has published so
	// far. The coordinator records them, marked interrupted, when the child
	// ends without an output (REQ-CONV-009, REQ-CONV-013).
	Partial(ctx context.Context, child runtime.RunRef) ([]ModelMessage, error)
}

// Options are the coordinator's dependencies. Turns may be nil when the
// runtime has no typed input channel; the record still changes, but the child
// is not steered or cancelled.
type Options struct {
	Turns    Turns
	Register func(name string, fn runtime.StepFunc) error
}

// Input starts a coordinator run. Record is nil for a new conversation and
// carries the same record across a continuation (REQ-CONV-014). Flow is the
// conversation's flow; TurnFlow is the flow of one turn (WP-L7-01), which every
// child run of the conversation uses.
type Input struct {
	Tenant   core.TenantID   `json:"tenant"`
	User     string          `json:"user"`
	ID       string          `json:"id"`
	Flow     runtime.FlowRef `json:"flow"`
	TurnFlow runtime.FlowRef `json:"turn_flow"`
	// TurnOut is the state key under which the turn flow's agent node writes
	// its output (WP-L7-01). A flow run returns its final state, so the
	// coordinator reads agent.Output from this key and records the turns the
	// agent added after the carried history as the turn's messages. Empty
	// means the turn flow's final state is itself a TurnOutput.
	TurnOut   string  `json:"turn_out,omitempty"`
	Threshold int     `json:"threshold,omitempty"`
	Record    *Record `json:"record,omitempty"`
}

// Output is a coordinator run's result: the record and, when the conversation
// was cancelled, its state.
type Output struct {
	State  string `json:"state"`
	Record Record `json:"record"`
}

// Coordinator is the conversation flow code (REQ-CONV-001).
type Coordinator struct {
	opts Options
}

// New returns a coordinator over opts.
func New(opts Options) *Coordinator { return &Coordinator{opts: opts} }

// Run serves one conversation until it is cancelled. It starts one child run
// per turn and never starts the next turn before the running child run has
// ended (REQ-CONV-002). A user message arrives as a gate resolution; while a
// turn runs, Run races the next message against that turn's child run.
func (c *Coordinator) Run(dc runtime.DurableContext, in Input) (Output, error) {
	if dc == nil {
		return Output{}, core.NewError(core.CodeInvalidArgument, "conversation: nil durable context", nil)
	}
	rec := NewRecord(in.Tenant, in.User, in.ID, in.Flow)
	if in.Record != nil {
		rec = *in.Record
	}
	// The count is the durable calls this run of the coordinator made itself
	// (REQ-CONV-014). A run starts it at zero, whether it is a new conversation
	// or a continuation; a replay recounts the same calls in the same order.
	rec.Calls = 0
	if rec.TurnFlow.Name == "" {
		rec.TurnFlow = in.TurnFlow
	}
	if rec.TurnOut == "" {
		rec.TurnOut = in.TurnOut
	}
	if rec.TurnFlow.Name == "" {
		return Output{}, core.NewError(core.CodeInvalidArgument, "conversation: no turn flow", map[string]any{"conversation": rec.ID})
	}
	gates, refused := 0, 0
	var child runtime.Future
	var childRef runtime.RunRef
	// carried is how many messages the running child got as history; the
	// agent's output repeats them in its turns, so only the turns after them
	// belong to this turn (REQ-CONV-012).
	carried := 0

	for {
		if rec.Cancelled {
			return Output{State: TurnCancelled, Record: rec}, nil
		}
		if rec.Running == 0 {
			if len(rec.Queue) == 0 {
				gates++
				msg, err := c.await(dc, &rec, gates)
				if err != nil {
					return c.stopped(dc, rec, childRef)
				}
				decision, err := rec.Accept(msg)
				if err != nil {
					refused++
					if err := c.refuse(dc, &rec, refused, msg, err); err != nil {
						return c.stopped(dc, rec, childRef)
					}
					continue
				}
				if decision.Kind == DecisionDuplicate {
					continue
				}
			}
			start, ok := rec.BeginTurn()
			if !ok {
				continue
			}
			// The child id lives in its own namespace: a conversation id is
			// unconstrained, so "<run id>-turn-<n>" could repeat another
			// conversation's run id, while no RunID ever starts with "turn@".
			childRef = runtime.RunRef{
				RunID:    fmt.Sprintf("turn@%s@%d", rec.ID, start.Index),
				TenantID: rec.Tenant,
			}
			rec.Turns[start.Index-1].Child = childRef.RunID
			if err := c.step(dc, &rec, fmt.Sprintf("record-turn-%d-start", start.Index), rec); err != nil {
				return c.stopped(dc, rec, childRef)
			}
			rec.DurableCall()
			carried = len(start.Input.Messages)
			child = dc.Child(rec.TurnFlow, turnFlowInput(start.Input), runtime.ChildOptions{ID: childRef.RunID, ParentClose: "request_cancel"})
			continue
		}

		gates++
		rec.DurableCall()
		inbox := dc.Await(runtime.GateSpec{Name: fmt.Sprintf("inbox-%d", gates)})
		rec.DurableCall()
		idx, err := dc.Select(inbox, child)
		if err != nil {
			return c.stopped(dc, rec, childRef)
		}
		if idx == 0 {
			var msg Message
			if err := inbox.Get(&msg); err != nil {
				return c.stopped(dc, rec, childRef)
			}
			decision, err := rec.Accept(msg)
			if err != nil {
				refused++
				if err := c.refuse(dc, &rec, refused, msg, err); err != nil {
					return c.stopped(dc, rec, childRef)
				}
				continue
			}
			if decision.Kind == DecisionDuplicate {
				continue
			}
			switch decision.Kind {
			case DecisionQueued:
				if err := c.step(dc, &rec, fmt.Sprintf("record-message-%d-%s", rec.Running, msg.ID), rec); err != nil {
					return c.stopped(dc, rec, childRef)
				}
			case DecisionSteer:
				rec.Steer(msg.ID, msg.Text)
				if err := c.step(dc, &rec, fmt.Sprintf("record-steer-%d-%s", rec.Running, msg.ID), rec); err != nil {
					return c.stopped(dc, rec, childRef)
				}
				if c.opts.Turns != nil {
					if err := c.opts.Turns.Steer(dc.Context(), childRef, msg.Text); err != nil {
						return c.undelivered(dc, rec, childRef, "steering message", msg, err)
					}
				}
			case DecisionCancelTurn:
				if err := c.step(dc, &rec, fmt.Sprintf("record-cancel-turn-%d-%s", rec.Running, msg.ID), rec); err != nil {
					return c.stopped(dc, rec, childRef)
				}
				if c.opts.Turns != nil {
					if err := c.opts.Turns.Cancel(dc.Context(), childRef, "user cancelled the turn"); err != nil {
						return c.undelivered(dc, rec, childRef, "cancel decision", msg, err)
					}
				}
			}
			continue
		}

		var raw json.RawMessage
		endErr := child.Get(&raw)
		var out TurnOutput
		if endErr == nil {
			out, endErr = decodeTurnResult(raw, rec.TurnOut, carried)
		}
		if endErr != nil {
			out.State = TurnFailed
			out.Code = string(core.CodeOf(endErr))
			if core.CodeOf(endErr) == core.CodeCancelled {
				out.State = TurnCancelled
			}
			if c.opts.Turns != nil {
				if messages, err := c.opts.Turns.Partial(dc.Context(), childRef); err == nil {
					out.Messages = messages
				}
			}
		}
		ended := rec.Turns[len(rec.Turns)-1].Index
		rec.EndTurn(out)
		if err := c.step(dc, &rec, fmt.Sprintf("record-turn-%d-end", ended), rec); err != nil {
			return c.stopped(dc, rec, childRef)
		}
		child = nil
		if rec.CheckpointDue(in.Threshold, dc.Capabilities()) {
			rec.DurableCall()
			// The runtime decides: a nil result is "did not continue", so the
			// count keeps growing; the ContinueAsNew error means this run
			// ended and the new one starts its own count (REQ-CONV-014).
			if err := dc.Checkpoint(runtime.SnapshotRef{Seq: int64(len(rec.Turns))}); err != nil {
				if dc.Context().Err() != nil {
					return c.stopped(dc, rec, childRef)
				}
				return Output{Record: rec}, err
			}
		}
	}
}

// await waits for the next user message at the conversation's inbox.
func (c *Coordinator) await(dc runtime.DurableContext, rec *Record, n int) (Message, error) {
	rec.DurableCall()
	f := dc.Await(runtime.GateSpec{Name: fmt.Sprintf("inbox-%d", n)})
	var msg Message
	if err := f.Get(&msg); err != nil {
		return Message{}, err
	}
	return msg, nil
}

// step records a state change of the conversation record as a durable step.
// The record itself lives in the run's state; journaling it makes the change
// replay-safe and leaves the record's history to the runtime.
func (c *Coordinator) step(dc runtime.DurableContext, rec *Record, name string, in any) error {
	if c.opts.Register != nil {
		if err := c.opts.Register(name, RecordStep); err != nil && core.CodeOf(err) != core.CodeConflict {
			return err
		}
	}
	rec.DurableCall()
	return dc.Step(name, in, runtime.StepOptions{IdempotencyKey: name}).Get(nil)
}

// refuse journals a message the coordinator refused. The gate resolution has
// already succeeded at the runtime, so the refusal is surfaced as a durable
// record of the edge's answer instead of being dropped (REQ-CONV-017 to
// REQ-CONV-021).
func (c *Coordinator) refuse(dc runtime.DurableContext, rec *Record, n int, msg Message, cause error) error {
	return c.step(dc, rec, fmt.Sprintf("refuse-%d", n), refusal{MessageID: msg.ID, Code: string(core.CodeOf(cause))})
}

// stopped ends Run when the runtime has cancelled the conversation or failed a
// wait. A cancelled context is the conversation's run being cancelled
// (REQ-CONV-011): the running turn is cancelled and its partial recorded, the
// queue is dropped, and the run reports cancelled. Anything else is a runtime
// failure.
func (c *Coordinator) stopped(dc runtime.DurableContext, rec Record, child runtime.RunRef) (Output, error) {
	if err := dc.Context().Err(); err != nil {
		if rec.Running != 0 && c.opts.Turns != nil {
			_ = c.opts.Turns.Cancel(dc.Context(), child, "conversation cancelled")
			out := TurnOutput{State: TurnCancelled, Code: string(core.CodeCancelled)}
			if messages, err := c.opts.Turns.Partial(dc.Context(), child); err == nil {
				out.Messages = messages
			}
			rec.EndTurn(out)
		}
		rec.CancelConversation()
		return Output{State: TurnCancelled, Record: rec}, nil
	}
	return Output{Record: rec}, core.NewError(core.CodeUnavailable, "conversation: the runtime stopped waiting", nil)
}

// undelivered ends the run when a steering or cancel decision the coordinator
// journaled did not reach the running child. The child may still be running,
// so this is not a stopped conversation: reporting one would be false and the
// caller would learn nothing about the control-plane failure. The run fails
// with the delivery's code, cause and retry semantics (REQ-CONV-007,
// REQ-CONV-008); a cancelled context is a conversation cancel (REQ-CONV-011)
// and keeps its path through stopped.
func (c *Coordinator) undelivered(dc runtime.DurableContext, rec Record, child runtime.RunRef, action string, msg Message, cause error) (Output, error) {
	if dc.Context().Err() != nil {
		return c.stopped(dc, rec, child)
	}
	code := core.CodeOf(cause)
	if code == core.CodeCancelled {
		// The delivery's cancellation is not the conversation's: a caller
		// must not read this failure as the conversation being cancelled.
		code = core.CodeUnavailable
	}
	return Output{}, &core.Error{
		Code:      code,
		Message:   fmt.Sprintf("conversation: the running turn did not receive the %s: %v", action, cause),
		Retryable: core.IsRetryable(cause),
		Details: map[string]any{
			"conversation": rec.ID,
			"turn":         rec.Running,
			"message":      msg.ID,
			"child":        child.RunID,
		},
		Cause: cause,
	}
}

// RecordStep is the step function of the coordinator's record steps. The
// record is part of the run's state; the step echoes its input so a replay
// returns the same journal value.
func RecordStep(_ context.Context, in json.RawMessage) (json.RawMessage, error) {
	return in, nil
}

// refusal is the durable record of a refused gate resolution.
type refusal struct {
	MessageID string `json:"message_id"`
	Code      string `json:"code"`
}

// turnFlowInput translates the history a turn sees into the input of the turn
// flow (design §1, §3). The turn flow is an agent run (WP-L7-01), whose input
// carries its history as a handoff: one model message per recorded message, in
// order. The starting user message is the last of them.
func turnFlowInput(in TurnInput) json.RawMessage {
	turns := make([]turnFlowTurn, 0, len(in.Messages))
	for _, m := range in.Messages {
		role := "user"
		if m.Role == RoleModel {
			role = "assistant"
		}
		turns = append(turns, turnFlowTurn{Message: turnFlowMessage{Role: role, Content: m.Text}})
	}
	raw, err := json.Marshal(turnFlowEnvelope{State: "handoff", Turns: turns})
	if err != nil {
		return nil
	}
	return raw
}

type turnFlowEnvelope struct {
	State string         `json:"state"`
	Turns []turnFlowTurn `json:"turns,omitempty"`
}

type turnFlowTurn struct {
	Message turnFlowMessage `json:"message"`
}

type turnFlowMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
