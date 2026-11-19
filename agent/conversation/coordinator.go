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
}

// Options are the coordinator's dependencies. Turns may be nil when the
// runtime has no typed input channel; the record still changes, but the child
// is not steered or cancelled.
type Options struct {
	Turns    Turns
	Register func(name string, fn runtime.StepFunc) error
}

// Input starts a coordinator run. Record is nil for a new conversation and
// carries the same record across a continuation (REQ-CONV-014).
type Input struct {
	Tenant    core.TenantID   `json:"tenant"`
	User      string          `json:"user"`
	ID        string          `json:"id"`
	Flow      runtime.FlowRef `json:"flow"`
	Threshold int             `json:"threshold,omitempty"`
	Record    *Record         `json:"record,omitempty"`
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
	gates := 0
	var child runtime.Future
	var childRef runtime.RunRef

	for {
		if rec.Cancelled {
			return Output{State: TurnCancelled, Record: rec}, nil
		}
		if rec.Running == 0 {
			if len(rec.Queue) == 0 {
				gates++
				msg, err := c.await(dc, &rec, gates)
				if err != nil {
					return c.stopped(dc, rec)
				}
				decision, err := rec.Accept(msg)
				if err != nil {
					// The edge validates synchronously (design §1); a message
					// that got this far is folded into the record only.
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
			if err := c.step(dc, &rec, fmt.Sprintf("record-turn-%d", start.Index), start.Input); err != nil {
				return c.stopped(dc, rec)
			}
			childRef = runtime.RunRef{
				RunID:    fmt.Sprintf("%s/turn-%d", RunID(rec.ID), start.Index),
				TenantID: rec.Tenant,
			}
			rec.DurableCall()
			child = dc.Child(rec.Flow, start.Input, runtime.ChildOptions{ID: childRef.RunID, ParentClose: "request_cancel"})
			continue
		}

		gates++
		rec.DurableCall()
		inbox := dc.Await(runtime.GateSpec{Name: fmt.Sprintf("inbox-%d", gates)})
		rec.DurableCall()
		idx, err := dc.Select(inbox, child)
		if err != nil {
			return c.stopped(dc, rec)
		}
		if idx == 0 {
			var msg Message
			if err := inbox.Get(&msg); err != nil {
				return c.stopped(dc, rec)
			}
			decision, err := rec.Accept(msg)
			if err != nil || decision.Kind == DecisionDuplicate {
				continue
			}
			switch decision.Kind {
			case DecisionSteer:
				rec.Steer(msg.ID, msg.Text)
				if c.opts.Turns != nil {
					if err := c.opts.Turns.Steer(dc.Context(), childRef, msg.Text); err != nil {
						return c.stopped(dc, rec)
					}
				}
			case DecisionCancelTurn:
				if c.opts.Turns != nil {
					if err := c.opts.Turns.Cancel(dc.Context(), childRef, "user cancelled the turn"); err != nil {
						return c.stopped(dc, rec)
					}
				}
			}
			continue
		}

		var out TurnOutput
		endErr := child.Get(&out)
		if endErr != nil {
			out.State = TurnFailed
			out.Code = string(core.CodeOf(endErr))
			if core.CodeOf(endErr) == core.CodeCancelled {
				out.State = TurnCancelled
			}
		}
		rec.EndTurn(out)
		if err := c.step(dc, &rec, fmt.Sprintf("record-turn-%d", rec.Turns[len(rec.Turns)-1].Index), rec.Turns[len(rec.Turns)-1]); err != nil {
			return c.stopped(dc, rec)
		}
		child = nil
		if rec.CheckpointDue(in.Threshold, dc.Capabilities()) {
			rec.DurableCall()
			if err := dc.Checkpoint(runtime.SnapshotRef{Seq: int64(rec.Continued)}); err != nil {
				return c.stopped(dc, rec)
			}
			rec.Checkpointed()
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

// step records a turn state change as a durable step. The record itself lives
// in the run's state; the step makes the change replay-safe.
func (c *Coordinator) step(dc runtime.DurableContext, rec *Record, name string, in any) error {
	if c.opts.Register != nil {
		if err := c.opts.Register(name, RecordStep); err != nil && core.CodeOf(err) != core.CodeConflict {
			return err
		}
	}
	rec.DurableCall()
	return dc.Step(name, in, runtime.StepOptions{IdempotencyKey: name}).Get(nil)
}

// stopped ends Run when the runtime has cancelled the conversation or failed a
// wait. A cancelled context is the conversation's run being cancelled
// (REQ-CONV-011); anything else is a runtime failure.
func (c *Coordinator) stopped(dc runtime.DurableContext, rec Record) (Output, error) {
	if err := dc.Context().Err(); err != nil {
		rec.Cancelled = true
		rec.Queue = nil
		return Output{State: TurnCancelled, Record: rec}, nil
	}
	return Output{Record: rec}, core.NewError(core.CodeUnavailable, "conversation: the runtime stopped waiting", nil)
}

// RecordStep is the step function of the coordinator's record-turn step. The
// record is part of the run's state; the step echoes its input so a replay
// returns the same journal value.
func RecordStep(_ context.Context, in json.RawMessage) (json.RawMessage, error) {
	return in, nil
}
